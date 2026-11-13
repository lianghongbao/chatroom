package biz

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"time"

	v1 "chatroom/api/conversation/service/v1"

	kratoserrors "github.com/go-kratos/kratos/v3/errors"
)

// Conversation 会话 DO。
type Conversation struct {
	ID          uint64
	Type        ConversationType
	Name        string
	OwnerID     string
	MemberIDs   []string // 成员 uid 列表，升序。单聊固定 2 人，群聊 >= 2 人
	MemberCount uint32   // 成员数
	// MemberNicknames / MemberAvatars 成员资料，与 MemberIDs 等长且下标对齐。
	// 某个成员资料没拉到时该位置为空字符串占位。
	MemberNicknames []string
	MemberAvatars   []string
	LastMessage     string    // 最后一条消息摘要
	LastMessageAt   time.Time // 最后一条消息时间
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// ConversationType 会话类型。
type ConversationType uint8

const (
	ConversationTypeSingle ConversationType = 1 // 单聊：固定两个成员
	ConversationTypeGroup  ConversationType = 2 // 群聊：成员可增减，群主唯一
)

// IsGroup 是否群聊。
func (t ConversationType) IsGroup() bool { return t == ConversationTypeGroup }

// 常量：会话规模与名字限制。
const (
	MaxGroupMembers        = 500 // 群聊成员数上限
	MaxConversationNameLen = 128 // 群名最大长度，与 ent schema 的 MaxLen 对齐
	maxListLimit           = 200 // 会话列表单页上限
	defaultListLimit       = 50  // 会话列表默认单页大小
	maxUserConversations   = 100 // 列某用户参与的所有会话时的上限
)

var (
	ErrConversationNotFound         = kratoserrors.NotFound(v1.ErrorReason_CONVERSATION_NOT_FOUND.String(), "conversation not found")
	ErrConversationInvalidArgument  = kratoserrors.BadRequest(v1.ErrorReason_CONVERSATION_INVALID_ARGUMENT.String(), "invalid conversation argument")
	ErrConversationNotMember        = kratoserrors.Forbidden(v1.ErrorReason_CONVERSATION_NOT_MEMBER.String(), "caller is not a member of this conversation")
	ErrConversationAlreadyMember    = kratoserrors.Conflict(v1.ErrorReason_CONVERSATION_ALREADY_MEMBER.String(), "already a member of this conversation")
	ErrConversationOwnerCannotLeave = kratoserrors.BadRequest(v1.ErrorReason_CONVERSATION_OWNER_CANNOT_LEAVE.String(), "conversation owner cannot leave")
	ErrConversationNotOwner         = kratoserrors.Forbidden(v1.ErrorReason_CONVERSATION_NOT_OWNER.String(), "caller is not the owner of this conversation")
	ErrConversationMemberLimit      = kratoserrors.BadRequest(v1.ErrorReason_CONVERSATION_MEMBER_LIMIT.String(), "conversation member limit reached")
	ErrConversationEmpty            = kratoserrors.BadRequest(v1.ErrorReason_CONVERSATION_EMPTY.String(), "conversation must have at least one member")
	ErrConversationNotGroup         = kratoserrors.BadRequest(v1.ErrorReason_CONVERSATION_NOT_GROUP.String(), "conversation is not a group chat")
	ErrConversationUserNotFound     = kratoserrors.BadRequest(v1.ErrorReason_CONVERSATION_USER_NOT_FOUND.String(), "one or more users do not exist")
	ErrConversationUnreadConflict   = kratoserrors.Conflict(v1.ErrorReason_CONVERSATION_UNREAD_CONFLICT.String(), "unread count update conflict")
)

// ConversationCursor 会话列表的游标。
// wire 上的游标只带会话 ID，repo 层拿到 ID 后反查那一行的 updated_at。
type ConversationCursor struct {
	ID uint64 // 上一页最后一条的会话 ID。0 表示首页
}

// IsZero 是否首页游标。
func (c ConversationCursor) IsZero() bool { return c.ID == 0 }

// ConversationRepo 会话仓储接口。
type ConversationRepo interface {
	// Save 新建会话 + 成员，返回落库后的会话。单聊复用已有会话时 reused 为 true。
	Save(ctx context.Context, c *Conversation) (saved *Conversation, reused bool, err error)
	// FindByID 按 id 查会话（含成员列表）。找不到返 ErrConversationNotFound。
	FindByID(ctx context.Context, id uint64) (*Conversation, error)
	// FindSingleByMembers 查两个成员之间的单聊。找不到返 ErrConversationNotFound。
	FindSingleByMembers(ctx context.Context, a, b string) (*Conversation, error)
	// ListByUserID 列出该用户参与的会话，按 updated_at DESC, id DESC。
	ListByUserID(ctx context.Context, userID string, cursor ConversationCursor, limit int) ([]*Conversation, error)
	// IsMember 判断 user 是否是会话成员。非成员返 (false, nil)。
	IsMember(ctx context.Context, conversationID uint64, userID string) (bool, error)
	// AddMembers 批量加成员，返回 (新增数, 加完后的成员总数)。
	AddMembers(ctx context.Context, conversationID uint64, userIDs []string) (added int, total int, err error)
	// RemoveMember 踢人，返回踢完后的成员总数。找不到成员返 ErrConversationNotFound。
	RemoveMember(ctx context.Context, conversationID uint64, userID string) (total int, err error)
	// UpdateName 改群名。找不到会话返 ErrConversationNotFound。
	UpdateName(ctx context.Context, id uint64, name string) (*Conversation, error)
	// TransferOwner 转让群主。新群主不在会里返 ErrConversationNotFound。
	TransferOwner(ctx context.Context, conversationID uint64, newOwnerID string) (*Conversation, error)
	// Dissolve 解散会话，连同全部成员记录一起删掉。找不到会话返 ErrConversationNotFound。
	Dissolve(ctx context.Context, conversationID uint64) error
	// TouchConversation 更新会话的活跃时间。
	TouchConversation(ctx context.Context, id uint64) error
	// UpdateUnread 增减某成员的会话级未读数，返回更新后的值。
	UpdateUnread(ctx context.Context, conversationID uint64, userID string, delta int32) (uint32, error)
	// UpdateUnreadBatch 批量增减多个成员的未读数，返回成功更新的成员数。不是成员的 uid 跳过。
	UpdateUnreadBatch(ctx context.Context, conversationID uint64, userIDs []string, delta int32) (uint32, error)
	// GetUnread 查某成员的会话级未读数。成员不存在返 ErrConversationNotFound。
	GetUnread(ctx context.Context, conversationID uint64, userID string) (uint32, error)
	// ClearUnread 把某成员的会话级未读数清零，返回清零后的值。
	ClearUnread(ctx context.Context, conversationID uint64, userID string) (uint32, error)
	// ListUnread 查某 uid 在所有会话的未读数，只返回未读数 > 0 的会话。
	ListUnread(ctx context.Context, userID string) (map[uint64]uint32, error)
}

// ConversationProfileFetcher 批量拉成员公开资料（跨服务 user 服务）。
// 整批失败返 (nil, nil, nil)，biz 走降级。
type ConversationProfileFetcher interface {
	// FetchProfiles 拉取给定 uid 的公开资料。ids 是要拉的 uid 列表。
	FetchProfiles(ctx context.Context, ids []string) ([]*UserProfile, error)
}

// UserExistenceChecker 校验一批 uid 是否都是真实存在的用户（跨服务 user 服务）。
// 校验失败时放行，user 服务不可达不能挡住正常业务。
type UserExistenceChecker interface {
	// CheckUsersExist 返回 id 集合中不存在的 uid 列表。ids 是待校验的 uid 列表。
	CheckUsersExist(ctx context.Context, ids []string) ([]string, error)
}

// UserProfile 成员的公开资料快照。conversation biz 只用昵称 / 头像。
type UserProfile struct {
	UserID   string
	Nickname string
	Avatar   string
}

// MemberInvalidationPublisher 成员变更通知（Redis Pub/Sub）。
// 通知 ws-gateway 失效本地成员缓存。发布失败不影响成员变更本身的结果。
type MemberInvalidationPublisher interface {
	// Publish 通知「会话 X 的成员变了」。conversationID 是会话 ID。
	Publish(ctx context.Context, conversationID uint64) error
}

// ConversationUsecase 会话用例编排。
type ConversationUsecase struct {
	repo      ConversationRepo
	profiles  ConversationProfileFetcher
	existence UserExistenceChecker
	publisher MemberInvalidationPublisher
	logger    *slog.Logger
}

// NewConversationUsecase 构造 ConversationUsecase。
// repo 仓储实现
// profiles nil 时走降级
// existence 为 nil 时跳过校验
// publisher 为 nil 时不发成员变更通知
// logger 为 nil 时用 slog.Default。
func NewConversationUsecase(repo ConversationRepo, profiles ConversationProfileFetcher, existence UserExistenceChecker, publisher MemberInvalidationPublisher, logger *slog.Logger) *ConversationUsecase {
	if logger == nil {
		logger = slog.Default()
	}
	return &ConversationUsecase{repo: repo, profiles: profiles, existence: existence, publisher: publisher, logger: logger}
}

// notifyMemberChange 通知网关「这个会话的成员变了」。conversationID 是会话 ID；失败只记日志。
func (uc *ConversationUsecase) notifyMemberChange(ctx context.Context, conversationID uint64) {
	if uc.publisher == nil {
		return
	}
	if err := uc.publisher.Publish(ctx, conversationID); err != nil {
		uc.logger.WarnContext(ctx, "成员变更通知失败", "err", err, "conversation_id", conversationID)
	}
}

// CreateConversation 创建会话。单聊幂等，重复调用返回同一个会话（reused=true）；群聊总是新建。
// callerID 调用方 uid
// memberIDs 成员 uid 列表
// typ 会话类型，为 UNSPECIFIED 时按成员数推断（2 人=单聊，>2 人=群聊）
// name 群名
func (uc *ConversationUsecase) CreateConversation(ctx context.Context, callerID string, memberIDs []string, typ ConversationType, name string) (*Conversation, bool, error) {
	if uc.repo == nil {
		return nil, false, ErrConversationInvalidArgument
	}
	if callerID == "" {
		return nil, false, ErrConversationInvalidArgument
	}

	members, err := normalizeMembers(callerID, memberIDs)
	if err != nil {
		return nil, false, err
	}

	// 类型推断：未指定时按成员数分单聊 / 群聊。
	if typ == 0 {
		if len(members) == 2 {
			typ = ConversationTypeSingle
		} else {
			typ = ConversationTypeGroup
		}
	}
	if typ != ConversationTypeSingle && typ != ConversationTypeGroup {
		return nil, false, ErrConversationInvalidArgument
	}
	if len(members) < 2 {
		return nil, false, ErrConversationInvalidArgument
	}
	// 单聊必须恰好两个人。多人单聊会让「两个成员之间的一条会话」这个语义
	// 失效 —— 消息列表、未读数都按会话粒度算，第三个人收不到也读不到。
	if typ == ConversationTypeSingle && len(members) != 2 {
		return nil, false, ErrConversationInvalidArgument
	}
	if len(members) > MaxGroupMembers {
		return nil, false, ErrConversationMemberLimit
	}

	// 单聊：先查已有会话，命中就直接复用，不写库。
	if typ == ConversationTypeSingle {
		existing, err := uc.repo.FindSingleByMembers(ctx, members[0], members[1])
		if err == nil && existing != nil {
			// 复用路径同样要补昵称/头像 —— 否则同一会话首次建带昵称、重复建不带，字段形状不一致。
			uc.fillProfiles(ctx, []*Conversation{existing})
			return existing, true, nil
		}
		if err != nil && !errors.Is(err, ErrConversationNotFound) {
			return nil, false, err
		}
	}

	name = strings.TrimSpace(name)
	if typ == ConversationTypeSingle {
		// 单聊不用群名，避免两边各自建单聊时名字不一致。
		name = ""
	} else {
		if len(name) > MaxConversationNameLen {
			return nil, false, ErrConversationInvalidArgument
		}
	}

	ownerID := ""
	if typ == ConversationTypeGroup {
		ownerID = callerID
	}

	// 校验成员真实存在。user 服务不可达时放行。
	if uc.existence != nil {
		missing, err := uc.existence.CheckUsersExist(ctx, members)
		if err != nil {
			uc.logger.WarnContext(ctx, "用户存在性校验失败，放行建会话", "err", err)
		} else if len(missing) > 0 {
			return nil, false, ErrConversationUserNotFound
		}
	}

	conv := &Conversation{
		Type:        typ,
		Name:        name,
		OwnerID:     ownerID,
		MemberIDs:   members,
		MemberCount: uint32(len(members)),
	}
	saved, reused, err := uc.repo.Save(ctx, conv)
	if err != nil {
		return nil, false, err
	}
	if saved == nil {
		saved = conv
	}

	// 补全成员昵称/头像（跨服务拉 user 资料）。拉不到就留空，不阻断建会话。
	uc.fillProfiles(ctx, []*Conversation{saved})
	return saved, reused, nil
}

// ListConversations 列出调用方参与的所有会话，按最后活动时间倒序。
// callerID 调用方 uid
// cursor 上一页游标（0 表示首页）
// limit 单页条数（<= 0 走默认 50）
// 返回会话列表与下一页游标，末页游标为 0
func (uc *ConversationUsecase) ListConversations(ctx context.Context, callerID string, cursor uint64, limit int) ([]*Conversation, uint64, error) {
	if uc.repo == nil {
		return nil, 0, ErrConversationInvalidArgument
	}
	if callerID == "" {
		return nil, 0, ErrConversationInvalidArgument
	}
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}

	// 多取一条来判断「是否还有下一页」—— 只取 limit 条时，末页拿满 limit
	// 无法区分「还有更多」和「刚好取完」。
	items, err := uc.repo.ListByUserID(ctx, callerID, decodeCursor(cursor), limit+1)
	if err != nil {
		return nil, 0, err
	}

	var nextCursor uint64
	if len(items) > limit {
		// 多出来的那条说明还有下一页。游标取本页最后一条（不是多出来那条）。
		nextCursor = encodeCursor(items[limit-1])
		items = items[:limit]
	}

	uc.fillProfiles(ctx, items)
	return items, nextCursor, nil
}

// encodeCursor 取会话 ID 作为下一页游标，nil 返 0（首页）。c 是会话。
func encodeCursor(c *Conversation) uint64 {
	if c == nil {
		return 0
	}
	return c.ID
}

// decodeCursor 把 wire 上的 uint64 游标转成 repo 层用的游标，0 视为首页。
// v 是 wire 游标。
func decodeCursor(v uint64) ConversationCursor {
	if v == 0 {
		return ConversationCursor{}
	}
	return ConversationCursor{ID: v}
}

// GetConversation 会话详情。仅会话成员可见，非成员折叠成 NOT_FOUND。
// callerID 调用方 uid
// conversationID 会话 ID
func (uc *ConversationUsecase) GetConversation(ctx context.Context, callerID string, conversationID uint64) (*Conversation, error) {
	if uc.repo == nil {
		return nil, ErrConversationInvalidArgument
	}
	if callerID == "" || conversationID == 0 {
		return nil, ErrConversationInvalidArgument
	}

	conv, err := uc.repo.FindByID(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	// 非成员看不到会话 —— 折叠成 NOT_FOUND，不泄露「会话存在但你不是成员」
	isMember, err := uc.repo.IsMember(ctx, conversationID, callerID)
	if err != nil {
		return nil, err
	}
	if !isMember {
		return nil, ErrConversationNotFound
	}

	uc.fillProfiles(ctx, []*Conversation{conv})
	return conv, nil
}

// AddMembers 往群聊加成员。仅群主可调用。
// callerID 调用方 uid
// conversationID 会话 ID
// memberIDs 要加的用户 uid
// 返回 (加完后的成员总数, 本次新增数)
func (uc *ConversationUsecase) AddMembers(ctx context.Context, callerID string, conversationID uint64, memberIDs []string) (uint32, uint32, error) {
	if uc.repo == nil {
		return 0, 0, ErrConversationInvalidArgument
	}
	if callerID == "" || conversationID == 0 {
		return 0, 0, ErrConversationInvalidArgument
	}
	if len(memberIDs) == 0 {
		return 0, 0, ErrConversationInvalidArgument
	}

	conv, err := uc.repo.FindByID(ctx, conversationID)
	if err != nil {
		return 0, 0, err
	}
	if !conv.Type.IsGroup() {
		return 0, 0, ErrConversationNotGroup
	}
	if conv.OwnerID != callerID {
		return 0, 0, ErrConversationNotOwner
	}

	// 去重 + 排除已在会里的。
	toAdd := make([]string, 0, len(memberIDs))
	seen := make(map[string]struct{}, len(memberIDs))
	existing := make(map[string]struct{}, len(conv.MemberIDs))
	for _, id := range conv.MemberIDs {
		existing[id] = struct{}{}
	}
	for _, id := range memberIDs {
		if id == "" {
			return 0, 0, ErrConversationInvalidArgument
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if _, ok := existing[id]; ok {
			continue
		}
		toAdd = append(toAdd, id)
	}
	if len(toAdd) == 0 {
		// 全都已在会里：不算错，走 repo 拿统一口径的成员数。
		_, total, err := uc.repo.AddMembers(ctx, conversationID, nil)
		if err != nil {
			return 0, 0, err
		}
		return uint32(total), 0, nil
	}
	if len(conv.MemberIDs)+len(toAdd) > MaxGroupMembers {
		return 0, 0, ErrConversationMemberLimit
	}

	// 校验要加的人真实存在。user 服务不可达时放行。
	if uc.existence != nil {
		missing, err := uc.existence.CheckUsersExist(ctx, toAdd)
		if err != nil {
			uc.logger.WarnContext(ctx, "用户存在性校验失败，放行加成员", "err", err, "conversation_id", conversationID)
		} else if len(missing) > 0 {
			return 0, 0, ErrConversationUserNotFound
		}
	}

	added, total, err := uc.repo.AddMembers(ctx, conversationID, toAdd)
	if err != nil {
		return 0, 0, err
	}
	// 成员变动 → 会话活跃时间更新，会话列表排序跟着变。
	if err := uc.repo.TouchConversation(ctx, conversationID); err != nil {
		uc.logger.WarnContext(ctx, "更新会话活跃时间失败", "err", err, "conversation_id", conversationID)
	}
	// 通知网关失效成员缓存 —— 新人加群后要能立刻收到消息。
	uc.notifyMemberChange(ctx, conversationID)
	return uint32(total), uint32(added), nil
}

// RemoveMember 踢人。仅群主可调用，且不能踢自己。
// callerID 调用方 uid
// conversationID 会话 ID
// memberID 被踢的用户 uid
// 返回踢完后的成员总数
func (uc *ConversationUsecase) RemoveMember(ctx context.Context, callerID string, conversationID uint64, memberID string) (uint32, error) {
	if uc.repo == nil {
		return 0, ErrConversationInvalidArgument
	}
	if callerID == "" || conversationID == 0 || memberID == "" {
		return 0, ErrConversationInvalidArgument
	}

	conv, err := uc.repo.FindByID(ctx, conversationID)
	if err != nil {
		return 0, err
	}
	if !conv.Type.IsGroup() {
		return 0, ErrConversationNotGroup
	}
	if conv.OwnerID != callerID {
		return 0, ErrConversationNotOwner
	}
	if callerID == memberID {
		// 群主想退群走 LeaveConversation，别用踢人绕。
		return 0, ErrConversationInvalidArgument
	}
	// 踢完会话不能空。当前只有 1 个成员（就是群主自己）时踢不动
	if conv.MemberCount <= 1 {
		return 0, ErrConversationEmpty
	}

	total, err := uc.repo.RemoveMember(ctx, conversationID, memberID)
	if err != nil {
		return 0, err
	}
	if err := uc.repo.TouchConversation(ctx, conversationID); err != nil {
		uc.logger.WarnContext(ctx, "更新会话活跃时间失败", "err", err, "conversation_id", conversationID)
	}
	// 通知网关失效成员缓存 —— 被踢的人不该再收到消息。
	uc.notifyMemberChange(ctx, conversationID)
	return uint32(total), nil
}

// LeaveConversation 主动退群。单聊不支持退群，群主需先转让群主或解散群聊。
// callerID 调用方 uid
// conversationID 会话 ID
func (uc *ConversationUsecase) LeaveConversation(ctx context.Context, callerID string, conversationID uint64) error {
	if uc.repo == nil {
		return ErrConversationInvalidArgument
	}
	if callerID == "" || conversationID == 0 {
		return ErrConversationInvalidArgument
	}

	conv, err := uc.repo.FindByID(ctx, conversationID)
	if err != nil {
		return err
	}
	if !conv.Type.IsGroup() {
		return ErrConversationNotGroup
	}
	if conv.OwnerID == callerID {
		return ErrConversationOwnerCannotLeave
	}
	// 退群后会话不能空。
	if conv.MemberCount <= 1 {
		return ErrConversationEmpty
	}

	if _, err := uc.repo.RemoveMember(ctx, conversationID, callerID); err != nil {
		return err
	}
	if err := uc.repo.TouchConversation(ctx, conversationID); err != nil {
		uc.logger.WarnContext(ctx, "更新会话活跃时间失败", "err", err, "conversation_id", conversationID)
	}
	// 通知网关失效成员缓存 —— 退群的人不该再收到消息。
	uc.notifyMemberChange(ctx, conversationID)
	return nil
}

// TransferOwner 转让群主。仅当前群主可调用，新群主必须是群成员。
// callerID 调用方 uid
// conversationID 会话 ID
// newOwnerID 新群主的 uid
func (uc *ConversationUsecase) TransferOwner(ctx context.Context, callerID string, conversationID uint64, newOwnerID string) (*Conversation, error) {
	if uc.repo == nil {
		return nil, ErrConversationInvalidArgument
	}
	if callerID == "" || conversationID == 0 || newOwnerID == "" {
		return nil, ErrConversationInvalidArgument
	}

	conv, err := uc.repo.FindByID(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	if !conv.Type.IsGroup() {
		return nil, ErrConversationNotGroup
	}
	if conv.OwnerID != callerID {
		return nil, ErrConversationNotOwner
	}
	if newOwnerID == callerID {
		// 已经是群主，不用转。
		return nil, ErrConversationAlreadyMember
	}

	updated, err := uc.repo.TransferOwner(ctx, conversationID, newOwnerID)
	if err != nil {
		return nil, err
	}
	// 不用额外 TouchConversation —— repo 的 TransferOwner 会更新 conversations 行。
	return updated, nil
}

// DissolveConversation 解散群聊。仅群主可调用，单聊不支持；解散后会话连同全部成员记录一起删除。
// callerID 调用方 uid
// conversationID 会话 ID
func (uc *ConversationUsecase) DissolveConversation(ctx context.Context, callerID string, conversationID uint64) error {
	if uc.repo == nil {
		return ErrConversationInvalidArgument
	}
	if callerID == "" || conversationID == 0 {
		return ErrConversationInvalidArgument
	}

	conv, err := uc.repo.FindByID(ctx, conversationID)
	if err != nil {
		return err
	}
	if !conv.Type.IsGroup() {
		return ErrConversationNotGroup
	}
	if conv.OwnerID != callerID {
		return ErrConversationNotOwner
	}

	if err := uc.repo.Dissolve(ctx, conversationID); err != nil {
		return err
	}
	// 成员全被清了 —— 通知网关失效缓存。
	uc.notifyMemberChange(ctx, conversationID)
	return nil
}

// ListMembers 列出会话成员。仅会话成员可见，非成员返 ErrConversationNotFound。
// callerID 调用方 uid
// conversationID 会话 ID
func (uc *ConversationUsecase) ListMembers(ctx context.Context, callerID string, conversationID uint64) (*Conversation, error) {
	if uc.repo == nil {
		return nil, ErrConversationInvalidArgument
	}
	if callerID == "" || conversationID == 0 {
		return nil, ErrConversationInvalidArgument
	}

	conv, err := uc.repo.FindByID(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	isMember, err := uc.repo.IsMember(ctx, conversationID, callerID)
	if err != nil {
		return nil, err
	}
	// 非成员看不到会话 —— 折叠成 NOT_FOUND，不泄露「会话存在但你不是成员」。
	// 和 GetConversation 保持一致。
	if !isMember {
		return nil, ErrConversationNotFound
	}

	// 成员资料是补充信息，拉不到就留空占位，不影响主路径。
	uc.fillProfiles(ctx, []*Conversation{conv})
	return conv, nil
}

// UpdateConversation 改群名。仅群主可调用，单聊不支持。
// callerID 调用方 uid
// conversationID 会话 ID
// name 新群名
func (uc *ConversationUsecase) UpdateConversation(ctx context.Context, callerID string, conversationID uint64, name string) (*Conversation, error) {
	if uc.repo == nil {
		return nil, ErrConversationInvalidArgument
	}
	if callerID == "" || conversationID == 0 {
		return nil, ErrConversationInvalidArgument
	}

	// 先查会话再校验参数 —— 非群主改群名报 NOT_OWNER，而不是被参数校验先拦下报 INVALID_ARGUMENT。
	conv, err := uc.repo.FindByID(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	if !conv.Type.IsGroup() {
		return nil, ErrConversationNotGroup
	}
	if conv.OwnerID != callerID {
		return nil, ErrConversationNotOwner
	}

	name = strings.TrimSpace(name)
	if name == "" || len(name) > MaxConversationNameLen {
		return nil, ErrConversationInvalidArgument
	}

	updated, err := uc.repo.UpdateName(ctx, conversationID, name)
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// CheckMembership 查某 uid 是不是会话成员。会话不存在和「不是成员」都返 false。
// conversationID 会话 ID
// userID 待查的用户 uid
func (uc *ConversationUsecase) CheckMembership(ctx context.Context, conversationID uint64, userID string) (bool, error) {
	if uc.repo == nil {
		return false, ErrConversationInvalidArgument
	}
	if conversationID == 0 || userID == "" {
		return false, ErrConversationInvalidArgument
	}
	return uc.repo.IsMember(ctx, conversationID, userID)
}

// UpdateUnread 增减某成员的会话级未读数，返回更新后的值。delta 为正表示新增未读，为负表示扣减，不会减到 0 以下。
// conversationID 会话 ID
// userID 成员 uid
func (uc *ConversationUsecase) UpdateUnread(ctx context.Context, conversationID uint64, userID string, delta int32) (uint32, error) {
	if uc.repo == nil {
		return 0, ErrConversationInvalidArgument
	}
	if conversationID == 0 || userID == "" {
		return 0, ErrConversationInvalidArgument
	}
	return uc.repo.UpdateUnread(ctx, conversationID, userID, delta)
}

// UpdateUnreadBatch 批量增减多个成员的会话级未读数，返回成功更新的成员数。
// conversationID 会话 ID
// userIDs 成员 uid 列表
// delta 增减量；不是成员的 uid 直接跳过
func (uc *ConversationUsecase) UpdateUnreadBatch(ctx context.Context, conversationID uint64, userIDs []string, delta int32) (uint32, error) {
	if uc.repo == nil {
		return 0, ErrConversationInvalidArgument
	}
	if conversationID == 0 || len(userIDs) == 0 {
		return 0, ErrConversationInvalidArgument
	}
	return uc.repo.UpdateUnreadBatch(ctx, conversationID, userIDs, delta)
}

// GetUnread 查某成员的会话级未读数。
// conversationID 会话 ID
// userID 成员 uid
func (uc *ConversationUsecase) GetUnread(ctx context.Context, conversationID uint64, userID string) (uint32, error) {
	if uc.repo == nil {
		return 0, ErrConversationInvalidArgument
	}
	if conversationID == 0 || userID == "" {
		return 0, ErrConversationInvalidArgument
	}
	return uc.repo.GetUnread(ctx, conversationID, userID)
}

// ClearUnread 把某成员的会话级未读数清零。
// conversationID 会话 ID
// userID 成员 uid
func (uc *ConversationUsecase) ClearUnread(ctx context.Context, conversationID uint64, userID string) (uint32, error) {
	if uc.repo == nil {
		return 0, ErrConversationInvalidArgument
	}
	if conversationID == 0 || userID == "" {
		return 0, ErrConversationInvalidArgument
	}
	return uc.repo.ClearUnread(ctx, conversationID, userID)
}

// ListUnread 查某 uid 在所有会话的未读数（key = conversation_id），只返回未读数 > 0 的会话。
// userID 是用户 uid。
func (uc *ConversationUsecase) ListUnread(ctx context.Context, userID string) (map[uint64]uint32, error) {
	if uc.repo == nil {
		return nil, ErrConversationInvalidArgument
	}
	if userID == "" {
		return nil, ErrConversationInvalidArgument
	}
	return uc.repo.ListUnread(ctx, userID)
}

// ListConversationMembers 列出会话的全部成员（服务间内部接口）。
// conversationID 是会话 ID。
func (uc *ConversationUsecase) ListConversationMembers(ctx context.Context, conversationID uint64) ([]string, error) {
	if uc.repo == nil {
		return nil, ErrConversationInvalidArgument
	}
	if conversationID == 0 {
		return nil, ErrConversationInvalidArgument
	}
	conv, err := uc.repo.FindByID(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	return conv.MemberIDs, nil
}

// ListUserConversations 列某个用户参与的所有会话 id（服务间内部接口）。
// 一次最多列 maxUserConversations 个会话。userID 是用户 uid。
func (uc *ConversationUsecase) ListUserConversations(ctx context.Context, userID string) ([]uint64, error) {
	if uc.repo == nil {
		return nil, ErrConversationInvalidArgument
	}
	if userID == "" {
		return nil, ErrConversationInvalidArgument
	}
	convs, err := uc.repo.ListByUserID(ctx, userID, ConversationCursor{}, maxUserConversations)
	if err != nil {
		return nil, err
	}
	ids := make([]uint64, 0, len(convs))
	for _, c := range convs {
		ids = append(ids, c.ID)
	}
	return ids, nil
}

// fillProfiles 批量拉成员资料填进 conv，整批 err 降级，单条缺省静默。convs 是待补全资料的会话列表。
func (uc *ConversationUsecase) fillProfiles(ctx context.Context, convs []*Conversation) {
	if uc.profiles == nil || len(convs) == 0 {
		return
	}
	// 一次会话列表里可能有几十个会话、上百个成员，合并去重后批量拉。
	seen := make(map[string]struct{})
	ids := make([]string, 0, len(convs)*2)
	for _, c := range convs {
		if c == nil {
			continue
		}
		for _, id := range c.MemberIDs {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return
	}

	profiles, err := uc.profiles.FetchProfiles(ctx, ids)
	if err != nil {
		uc.logger.WarnContext(ctx, "会话成员资料补全降级", "err", err, "count", len(convs))
		// 拉资料整体失败 —— 昵称/头像留空，但数组长度仍要跟 MemberIDs 对齐。
		fillEmptyProfiles(convs)
		return
	}
	byID := make(map[string]*UserProfile, len(profiles))
	for _, p := range profiles {
		if p == nil {
			continue
		}
		byID[p.UserID] = p
	}
	for _, c := range convs {
		if c == nil || len(c.MemberIDs) == 0 {
			continue
		}
		nicknames := make([]string, 0, len(c.MemberIDs))
		avatars := make([]string, 0, len(c.MemberIDs))
		for _, id := range c.MemberIDs {
			p := byID[id]
			if p == nil {
				// 该成员资料没拉到 —— 留空占位，保持与 MemberIDs 的下标对应。
				nicknames = append(nicknames, "")
				avatars = append(avatars, "")
				continue
			}
			nicknames = append(nicknames, p.Nickname)
			avatars = append(avatars, p.Avatar)
		}
		c.MemberNicknames = nicknames
		c.MemberAvatars = avatars
	}
}

// fillEmptyProfiles 把每个会话的昵称/头像数组填成与 MemberIDs 等长的空串。convs 是待处理的会话列表。
func fillEmptyProfiles(convs []*Conversation) {
	for _, c := range convs {
		if c == nil || len(c.MemberIDs) == 0 {
			continue
		}
		c.MemberNicknames = make([]string, len(c.MemberIDs))
		c.MemberAvatars = make([]string, len(c.MemberIDs))
	}
}

// normalizeMembers 把调用方并入成员列表，去重并升序。
// callerID 调用方 uid
// memberIDs 请求里的成员 uid 列表
func normalizeMembers(callerID string, memberIDs []string) ([]string, error) {
	seen := make(map[string]struct{}, len(memberIDs)+1)
	out := make([]string, 0, len(memberIDs)+1)

	seen[callerID] = struct{}{}
	out = append(out, callerID)

	for _, id := range memberIDs {
		if id == "" {
			return nil, ErrConversationInvalidArgument
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}
