package biz

import (
	"context"
	"log/slog"
	"time"

	v1 "chatroom/api/message/service/v1"
	"chatroom/pkg/oss"

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
	MessageTypeEmoji MessageType = 3 // 表情消息 —— Content 存表情字符
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
	// Create 落库一条消息，返回带 id 与 created_at 的消息。
	// m 待存的领域对象
	Create(ctx context.Context, m *Message) (*Message, error)
	// ListByConversation 拉历史消息，按 id 倒序（新的在前）。返回 (消息列表, 下一页游标)，没有更多时游标为 0。
	// conversationID 会话 ID
	// cursor 上一页最后一条消息的 id，0 表示从最新一条开始
	// limit 单页条数
	ListByConversation(ctx context.Context, conversationID uint64, cursor uint64, limit int) ([]*Message, uint64, error)
	// ListSince 拉「id 严格大于 sinceID」的消息，按 id 正序（旧的在前）。返回 (消息列表, 最后一条消息的 id, 还有更多吗)。
	// conversationID 会话 ID
	// sinceID 0 表示从头拉
	// limit 单页条数
	ListSince(ctx context.Context, conversationID, sinceID uint64, limit int) ([]*Message, uint64, bool, error)
}

// ConversationMembershipChecker 校验调用方是不是会话成员（跨服务 conversation 服务）。
type ConversationMembershipChecker interface {
	// IsMember 返回调用方是不是该会话的成员，会话不存在时也返 false。
	// conversationID 会话 ID
	// userID 用户 ID
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
	// oss OSS 客户端，为 nil 时关闭聊天图片上传。
	oss *oss.Client
	// imageExts 聊天图片允许的后缀白名单。
	imageExts []string
	// imageMaxSize 聊天图片最大字节数，0 表示不限。
	imageMaxSize int64
}

// NewMessageUsecase 构造 MessageUsecase。
// repo 消息仓储
// membership 成员校验，为 nil 时成员校验一律拒绝
// publisher 广播器
// unread 未读数读写
// lister 会话成员列举
// storage 对象存储能力，为零值时关闭聊天图片上传
// logger 日志器，为 nil 时用默认 logger
func NewMessageUsecase(repo MessageRepo, membership ConversationMembershipChecker, publisher MessagePublisher, unread UnreadCounter, lister ConversationMemberLister, storage StorageOptions, logger *slog.Logger) *MessageUsecase {
	if logger == nil {
		logger = slog.Default()
	}
	return &MessageUsecase{
		repo:          repo,
		membership:    membership,
		publisher:     publisher,
		unread:        unread,
		lister:        lister,
		logger:        logger,
		oss:           storage.Client,
		imageExts:     storage.AllowedExts,
		imageMaxSize:  storage.MaxSize,
	}
}

// SendMessage 发消息。
// callerID 发送者 ID
// conversationID 会话 ID
// typ 消息类型
// content 消息正文
// 顺序：校验成员身份 → 落库 → 广播。广播失败不影响落库结果。
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
	if typ != MessageTypeText && typ != MessageTypeImage && typ != MessageTypeEmoji {
		return nil, ErrMessageInvalidArgument
	}
	if content == "" {
		return nil, ErrMessageInvalidArgument
	}
	if len([]rune(content)) > MaxMessageContentLen {
		return nil, ErrMessageContentTooLong
	}

	// 成员校验先做 —— 不是会员就不该有任何后续动作。
	//
	// 顺序有讲究：把图片 / 表情的内容校验放在成员校验**之后**，
	// 否则没权限的人能靠错误码差异探测会话存不存在
	// （MESSAGE_NOT_MEMBER vs MESSAGE_UPLOAD_URL_INVALID）。
	if err := uc.checkMembership(ctx, callerID, conversationID); err != nil {
		return nil, err
	}

	// 表情消息的正文必须真的是表情 —— 单个 Unicode 字符，或带变体选择符的
	// 组合序列（如 "❤️" 是 U+2764 U+FE0F 两个码位）。
	//
	// 不校验的话，客户端能拿表情类型发任意文本 —— 消息列表里就出现
	// 「类型是表情、内容是一整段话」的记录，渲染逻辑没法区分。
	if typ == MessageTypeEmoji {
		if !isEmojiContent(content) {
			return nil, ErrMessageInvalidArgument
		}
	}
	// 图片消息的正文必须是本服务签发的对象地址，且对象得真的存在。
	//
	// 不校验的话，任何人都能把外链（甚至内网地址）当图片发进会话 ——
	// 拿到消息列表的人会去拉那个地址。
	if typ == MessageTypeImage {
		clean, err := uc.ValidateImageURL(ctx, callerID, content)
		if err != nil {
			return nil, err
		}
		content = clean
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

// incUnreadForMembers 给会话里除发送方以外的所有成员加一条未读。失败只记日志不往上抛。
// conversationID 会话 ID
// senderID 发送者 ID（自己不给自己加）
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

// ListMessages 拉历史消息。返回 (消息列表, 下一页游标)。
// callerID 调用者 ID
// conversationID 会话 ID
// cursor 上一页最后一条消息的 id，0 表示从最新开始
// limit 单页条数
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

// ListMessagesSince 拉「比某个 id 更新的」消息，按 id 正序。
// callerID 调用者 ID
// conversationID 会话 ID
// sinceID 断线前看到的最后一条消息 id
// limit 单页条数
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

// checkMembership 校验调用方是会话成员。conversation 服务不可达或 membership == nil 时一律拒绝，不放行。
// callerID 调用者 ID
// conversationID 会话 ID
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
	// GetUnread 返回调用方在该会话的未读数。
	// conversationID 会话 ID
	// userID 用户 ID
	GetUnread(ctx context.Context, conversationID uint64, userID string) (uint32, error)
	// IncUnread 给某成员的会话级未读数加 delta，返回更新后的值。
	// conversationID 会话 ID
	// userID 成员 uid
	// delta 增量
	IncUnread(ctx context.Context, conversationID uint64, userID string, delta int32) (uint32, error)
	// IncUnreadBatch 给一批成员的会话级未读数加 delta，整个 fan-out 压成一次调用。
	// conversationID 会话 ID
	// userIDs 成员 uid 列表
	// delta 增量
	IncUnreadBatch(ctx context.Context, conversationID uint64, userIDs []string, delta int32) (uint32, error)
	// ClearUnread 把调用方在该会话的未读数清零，返回清零后的值。
	// conversationID 会话 ID
	// userID 用户 ID
	ClearUnread(ctx context.Context, conversationID uint64, userID string) (uint32, error)
	// ListUnread 返回调用方所有会话的未读数（key = conversation_id）。
	// userID 用户 ID
	ListUnread(ctx context.Context, userID string) (map[uint64]uint32, error)
}

// ConversationMemberLister 列出会话成员（跨服务 conversation 服务）。
type ConversationMemberLister interface {
	// ListConversationMembers 返回会话成员 uid 列表。
	// conversationID 会话 ID
	ListConversationMembers(ctx context.Context, conversationID uint64) ([]string, error)
}

// MarkRead 清零某个会话的未读数。
// callerID 调用者 ID
// conversationID 会话 ID
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

// unreadDownstreamError 把未读数下游的错误映射成 503。下游不可达时 503 才准确；「成员不存在」是调用方的错，保留原始 404。
// err 下游返回的错误
// conversationID 会话 ID（仅用于日志）
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

// GetUnreadCount 查未读数。
// conversationID 为 0 时返回所有会话的未读总数（角标用）。返回 (未读数, 各会话未读数)，查单个会话时第二个返回值为 nil。
// callerID 调用者 ID
// conversationID 会话 ID，0 表示查所有会话
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
