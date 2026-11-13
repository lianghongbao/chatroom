package biz

import (
	"context"
	"log/slog"
	"time"

	v1 "chatroom/api/message/service/v1"

	kratoserrors "github.com/go-kratos/kratos/v3/errors"
)

// Message 消息 DO。
type Message struct {
	ID             uint64
	ConversationID uint64
	SenderID       string
	Type           MessageType
	Content        string
	CreatedAt      time.Time
}

// MessageType 消息类型。
type MessageType uint8

const (
	MessageTypeText  MessageType = 1 // 文本消息
	MessageTypeImage MessageType = 2 // 图片消息 —— Content 存图片 URL
)

// 消息内容限制与分页参数。
const (
	MaxMessageContentLen    = 4000 // 消息内容最大长度（字符）。超长直接拒绝，不截断
	MaxMessageListLimit     = 200  // 历史消息单页上限。data 层兜底夹一次
	DefaultMessageListLimit = 50   // 历史消息默认单页大小
)

var (
	ErrMessageNotFound                = kratoserrors.NotFound(v1.ErrorReason_MESSAGE_NOT_FOUND.String(), "message not found")
	ErrMessageInvalidArgument         = kratoserrors.BadRequest(v1.ErrorReason_MESSAGE_INVALID_ARGUMENT.String(), "invalid message argument")
	ErrMessageNotMember               = kratoserrors.Forbidden(v1.ErrorReason_MESSAGE_NOT_MEMBER.String(), "caller is not a member of this conversation")
	ErrMessageContentTooLong          = kratoserrors.BadRequest(v1.ErrorReason_MESSAGE_CONTENT_TOO_LONG.String(), "message content too long")
	ErrMessageConversationUnavailable = kratoserrors.ServiceUnavailable(v1.ErrorReason_MESSAGE_CONVERSATION_UNAVAILABLE.String(), "conversation service unavailable")
)

// MessageRepo 消息仓储接口。
type MessageRepo interface {
	// Create 落库一条消息。m 是待存的领域对象，返回带 id 与 created_at 的消息。
	Create(ctx context.Context, m *Message) (*Message, error)
	// ListByConversation 拉历史消息，按 id 倒序（新的在前）。conversationID 是会话 ID，
	// cursor 的语义是「比这个 id 更旧的」，cursor 为 0 从最新一条开始，limit 是单页条数。
	// 返回 (消息列表, 下一页游标)，没有更多时游标为 0。
	ListByConversation(ctx context.Context, conversationID uint64, cursor uint64, limit int) ([]*Message, uint64, error)
	// ListSince 拉「id 严格大于 sinceID」的消息，按 id 正序（旧的在前）。conversationID 是会话 ID，
	// sinceID 为 0 表示从头拉，limit 是单页条数。
	// 返回 (消息列表, 最后一条消息的 id, 还有更多吗)。
	ListSince(ctx context.Context, conversationID, sinceID uint64, limit int) ([]*Message, uint64, bool, error)
}

// ConversationMembershipChecker 校验调用方是不是会话成员（跨服务 conversation 服务）。
type ConversationMembershipChecker interface {
	// IsMember 返回调用方是不是该会话的成员。conversationID 是会话 ID，userID 是用户 ID。
	// 会话不存在时也返 false。
	IsMember(ctx context.Context, conversationID uint64, userID string) (bool, error)
}

// MessagePublisher 消息广播（Redis Pub/Sub）。
// 广播失败不影响落库；Pub/Sub 不持久化，订阅方掉线期间的消息会丢。
type MessagePublisher interface {
	// Publish 把消息发到该会话的频道。m 是要广播的消息。
	Publish(ctx context.Context, m *Message) error
}

// MessageUsecase 消息用例。
type MessageUsecase struct {
	repo       MessageRepo
	membership ConversationMembershipChecker
	publisher  MessagePublisher
	unread     UnreadCounter
	lister     ConversationMemberLister
	logger     *slog.Logger
}

// NewMessageUsecase 构造 MessageUsecase。repo 是消息仓储，membership 是成员校验，
// publisher 是广播器，unread 是未读数读写，lister 是会话成员列举，
// logger 为 nil 时用默认 logger。membership 为 nil 时成员校验一律拒绝。
func NewMessageUsecase(repo MessageRepo, membership ConversationMembershipChecker, publisher MessagePublisher, unread UnreadCounter, lister ConversationMemberLister, logger *slog.Logger) *MessageUsecase {
	if logger == nil {
		logger = slog.Default()
	}
	return &MessageUsecase{
		repo:       repo,
		membership: membership,
		publisher:  publisher,
		unread:     unread,
		lister:     lister,
		logger:     logger,
	}
}

// SendMessage 发消息。callerID 是发送者 ID，conversationID 是会话 ID，typ 是消息类型，content 是正文。
// 顺序：校验成员身份 → 落库 → 广播。
// 广播在落库之后，避免客户端收到查不到的消息。
func (uc *MessageUsecase) SendMessage(ctx context.Context, callerID string, conversationID uint64, typ MessageType, content string) (*Message, error) {
	if uc.repo == nil {
		return nil, ErrMessageInvalidArgument
	}
	if callerID == "" || conversationID == 0 {
		return nil, ErrMessageInvalidArgument
	}
	if typ == 0 {
		typ = MessageTypeText
	}
	if typ != MessageTypeText && typ != MessageTypeImage {
		return nil, ErrMessageInvalidArgument
	}
	if content == "" {
		return nil, ErrMessageInvalidArgument
	}
	if len([]rune(content)) > MaxMessageContentLen {
		return nil, ErrMessageContentTooLong
	}

	if err := uc.checkMembership(ctx, callerID, conversationID); err != nil {
		return nil, err
	}

	saved, err := uc.repo.Create(ctx, &Message{
		ConversationID: conversationID,
		SenderID:       callerID,
		Type:           typ,
		Content:        content,
	})
	if err != nil {
		return nil, err
	}

	// 广播失败不影响落库结果
	if uc.publisher != nil {
		if err := uc.publisher.Publish(ctx, saved); err != nil {
			uc.logger.WarnContext(ctx, "消息广播失败，消息已落库",
				"err", err, "message_id", saved.ID, "conversation_id", conversationID)
		}
	}

	// 加未读数。失败只记日志，未读数只是角标
	uc.incUnreadForMembers(ctx, conversationID, callerID)
	return saved, nil
}

// incUnreadForMembers 给会话里除发送方以外的所有成员加一条未读。
// conversationID 是会话 ID，senderID 是发送者 ID（自己不给自己加）。失败只记日志不往上抛。
func (uc *MessageUsecase) incUnreadForMembers(ctx context.Context, conversationID uint64, senderID string) {
	if uc.unread == nil || uc.lister == nil {
		return
	}
	members, err := uc.lister.ListConversationMembers(ctx, conversationID)
	if err != nil {
		uc.logger.WarnContext(ctx, "列会话成员失败，未读数未增加",
			"err", err, "conversation_id", conversationID)
		return
	}
	// 发送方自己发的消息不算未读
	targets := make([]string, 0, len(members))
	for _, uid := range members {
		if uid == senderID {
			continue
		}
		targets = append(targets, uid)
	}
	if len(targets) == 0 {
		return
	}

	// 批量加 —— 一次跨进程调用，而不是 N 次（500 人的群就是 500 次往返）。
	if _, err := uc.unread.IncUnreadBatch(ctx, conversationID, targets, 1); err != nil {
		// 批量失败不直接放弃：退回逐个加，保证单个成员的失败不影响其他人。
		uc.logger.WarnContext(ctx, "批量增加未读数失败，退回逐个增加",
			"err", err, "conversation_id", conversationID, "count", len(targets))
		for _, uid := range targets {
			if _, err := uc.unread.IncUnread(ctx, conversationID, uid, 1); err != nil {
				uc.logger.WarnContext(ctx, "增加未读数失败",
					"err", err, "conversation_id", conversationID, "user_id", uid)
			}
		}
	}
}

// ListMessages 拉历史消息。callerID 是调用者 ID，conversationID 是会话 ID，
// cursor 是上一页最后一条消息的 id（0 表示从最新开始），limit 是单页条数。
// 返回 (消息列表, 下一页游标)。
func (uc *MessageUsecase) ListMessages(ctx context.Context, callerID string, conversationID uint64, cursor uint64, limit int) ([]*Message, uint64, error) {
	if uc.repo == nil {
		return nil, 0, ErrMessageInvalidArgument
	}
	if callerID == "" || conversationID == 0 {
		return nil, 0, ErrMessageInvalidArgument
	}
	if limit <= 0 {
		limit = DefaultMessageListLimit
	}
	if limit > MaxMessageListLimit {
		limit = MaxMessageListLimit
	}

	if err := uc.checkMembership(ctx, callerID, conversationID); err != nil {
		return nil, 0, err
	}

	return uc.repo.ListByConversation(ctx, conversationID, cursor, limit)
}

// ListMessagesSince 拉「比某个 id 更新的」消息，按 id 正序。callerID 是调用者 ID，
// conversationID 是会话 ID，sinceID 是断线前看到的最后一条消息 id，limit 是单页条数。
// 断线重连补消息用，与 ListMessages 方向相反。
// 返回 (消息列表, 最后一条消息的 id, 还有更多吗)。
func (uc *MessageUsecase) ListMessagesSince(ctx context.Context, callerID string, conversationID, sinceID uint64, limit int) ([]*Message, uint64, bool, error) {
	if uc.repo == nil {
		return nil, 0, false, ErrMessageInvalidArgument
	}
	if callerID == "" || conversationID == 0 {
		return nil, 0, false, ErrMessageInvalidArgument
	}
	if limit <= 0 {
		limit = DefaultMessageListLimit
	}
	if limit > MaxMessageListLimit {
		limit = MaxMessageListLimit
	}

	if err := uc.checkMembership(ctx, callerID, conversationID); err != nil {
		return nil, 0, false, err
	}

	return uc.repo.ListSince(ctx, conversationID, sinceID, limit)
}

// checkMembership 校验调用方是会话成员。callerID 是调用者 ID，conversationID 是会话 ID。
// conversation 服务不可达或 membership == nil 时一律拒绝，不放行。
func (uc *MessageUsecase) checkMembership(ctx context.Context, callerID string, conversationID uint64) error {
	if uc.membership == nil {
		uc.logger.ErrorContext(ctx, "成员校验能力缺失，拒绝请求", "conversation_id", conversationID)
		return ErrMessageConversationUnavailable
	}
	ok, err := uc.membership.IsMember(ctx, conversationID, callerID)
	if err != nil {
		uc.logger.WarnContext(ctx, "成员身份校验失败", "err", err, "conversation_id", conversationID)
		return ErrMessageConversationUnavailable
	}
	if !ok {
		return ErrMessageNotMember
	}
	return nil
}

// UnreadCounter 未读数读写（跨服务 conversation 服务）。
type UnreadCounter interface {
	// GetUnread 返回调用方在该会话的未读数。conversationID 是会话 ID，userID 是用户 ID。
	GetUnread(ctx context.Context, conversationID uint64, userID string) (uint32, error)
	// IncUnread 给某成员的会话级未读数加 delta，返回更新后的值。delta 是增量。
	IncUnread(ctx context.Context, conversationID uint64, userID string, delta int32) (uint32, error)
	// IncUnreadBatch 给一批成员的会话级未读数加 delta。conversationID 是会话 ID，
	// userIDs 是成员 ID 列表，delta 是增量。整个 fan-out 压成一次调用。
	IncUnreadBatch(ctx context.Context, conversationID uint64, userIDs []string, delta int32) (uint32, error)
	// ClearUnread 把调用方在该会话的未读数清零，返回清零后的值。
	ClearUnread(ctx context.Context, conversationID uint64, userID string) (uint32, error)
	// ListUnread 返回调用方所有会话的未读数（key = conversation_id）。
	ListUnread(ctx context.Context, userID string) (map[uint64]uint32, error)
}

// ConversationMemberLister 列出会话成员（跨服务 conversation 服务）。
type ConversationMemberLister interface {
	// ListConversationMembers 返回会话成员 uid 列表。conversationID 是会话 ID。
	ListConversationMembers(ctx context.Context, conversationID uint64) ([]string, error)
}

// MarkRead 清零某个会话的未读数。callerID 是调用者 ID，conversationID 是会话 ID。
func (uc *MessageUsecase) MarkRead(ctx context.Context, callerID string, conversationID uint64) (uint32, error) {
	if uc.unread == nil {
		return 0, ErrMessageInvalidArgument
	}
	if callerID == "" || conversationID == 0 {
		return 0, ErrMessageInvalidArgument
	}
	if err := uc.checkMembership(ctx, callerID, conversationID); err != nil {
		return 0, err
	}
	n, err := uc.unread.ClearUnread(ctx, conversationID, callerID)
	if err != nil {
		return 0, uc.unreadDownstreamError(ctx, err, conversationID)
	}
	return n, nil
}

// unreadDownstreamError 把未读数下游的错误映射成 503。err 是下游返回的错误，
// conversationID 是会话 ID（仅用于日志）。
// 下游不可达时 503 才准确；「成员不存在」是调用方的错，保留原始 404。
func (uc *MessageUsecase) unreadDownstreamError(ctx context.Context, err error, conversationID uint64) error {
	if err == nil {
		return nil
	}
	// 「成员不存在」保留原始 404。
	// 必须用 kratoserrors.FromError 而不是 errors.As —— 下游返回的
	// *status.Error 没有 Unwrap()，errors.As 匹配不上。
	if se := kratoserrors.FromError(err); se != nil && se.Code == 404 {
		return err
	}
	uc.logger.WarnContext(ctx, "未读数下游调用失败", "err", err, "conversation_id", conversationID)
	return ErrMessageConversationUnavailable
}

// GetUnreadCount 查未读数。callerID 是调用者 ID，conversationID 为 0 时返回所有会话的未读总数（角标用）。
// 返回 (未读数, 各会话未读数)。查单个会话时第二个返回值为 nil。
func (uc *MessageUsecase) GetUnreadCount(ctx context.Context, callerID string, conversationID uint64) (uint32, map[uint64]uint32, error) {
	if uc.unread == nil {
		return 0, nil, ErrMessageInvalidArgument
	}
	if callerID == "" {
		return 0, nil, ErrMessageInvalidArgument
	}
	if conversationID == 0 {
		per, err := uc.unread.ListUnread(ctx, callerID)
		if err != nil {
			return 0, nil, uc.unreadDownstreamError(ctx, err, conversationID)
		}
		var total uint32
		for _, n := range per {
			total += n
		}
		return total, per, nil
	}
	// 查单个会话也要先确认在会里，否则能探测别人会话的未读数
	if err := uc.checkMembership(ctx, callerID, conversationID); err != nil {
		return 0, nil, err
	}
	n, err := uc.unread.GetUnread(ctx, conversationID, callerID)
	if err != nil {
		return 0, nil, uc.unreadDownstreamError(ctx, err, conversationID)
	}
	return n, nil, nil
}
