package biz

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	v1 "chatroom/friend-service/api/friend/v1"

	kratoserrors "github.com/go-kratos/kratos/v3/errors"
)

// Friend 好友关系 DO。一条记录代表「user_id 对 friend_id 提过申请」。
// service 层按 caller_id 决定「自己」是 user_id 还是 friend_id，本层不映射。
type Friend struct {
	ID        uint64
	UserID    string
	FriendID  string
	Status    FriendStatus
	CreatedAt time.Time
	UpdatedAt time.Time
	Message   string         // 申请附言，0..200 字符。
	Profile   *FriendProfile // 仅 ListFriends 路径填充；nil = 未补全 / 对方不可见。
}

type FriendStatus uint8

const (
	FriendStatusPending  FriendStatus = 0
	FriendStatusAccepted FriendStatus = 1
	FriendStatusRejected FriendStatus = 2
)

// MaxApplyMessageLen 好友申请附言的最大长度（按 rune 计，不是字节）。
const MaxApplyMessageLen = 200

// truncateRunes 按 rune 截断字符串。
//
// 不能用 s[:n] —— 那是按字节切的，中文 / emoji 一个字符 3~4 字节，
// 从中间切断会留下半个字符，MySQL 存进去直接报 1366（非法 UTF-8）。
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	// rune 数不超过 max 就别动 —— 避免无谓的分配。
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	count := 0
	for i := range s {
		if count == max {
			return s[:i]
		}
		count++
	}
	return s
}

// FriendApplyResult Apply 的返回。重复申请 / 反向收尾时也带当前 status，
// 让 caller 区分新建 vs 已存在。
type FriendApplyResult struct {
	RequestID uint64
	Status    FriendStatus
}

// FriendProfile 好友公开资料快照，仅承载 listFriends 需要的字段
type FriendProfile struct {
	UserID    string
	Nickname  string
	Avatar    string
	CreatedAt time.Time
}

// FriendProfileFetcher 批量拉好友 profile。
// 整批失败返 (nil, nil, nil)，biz 走降级；user service 不可达不能阻断主路径。
type FriendProfileFetcher interface {
	FetchProfiles(ctx context.Context, callerID string, ids []string) ([]*FriendProfile, error)
}

// FriendRepo 好友仓储接口
type FriendRepo interface {
	// Save 新建关系，撞 uk_user_friend 时折叠为 ErrFriendAlreadyApplied
	Save(ctx context.Context, f *Friend) (uint64, error)
	// FindByID 按主键查，找不到返 ErrFriendNotFound
	FindByID(ctx context.Context, id uint64) (*Friend, error)
	// FindByPair 按正向 (user_id, friend_id) 查
	FindByPair(ctx context.Context, userID, friendID string) (*Friend, error)
	// FindBothDirections 一次性查 (a,b) 和 (b,a) 两条 record
	FindBothDirections(ctx context.Context, a, b string) (forward, reverse *Friend, err error)
	// FindAcceptedByUserIDAndID 过滤 status=ACCEPTED + id 升序游标分页。
	// 双向语义：caller 既可能是发起方 (user_id) 也可能是接收方 (friend_id)，
	// 返回的每条记录都把「对方」归一化到 FriendID，UserID 保留原始方向。
	FindAcceptedByUserIDAndID(ctx context.Context, userID string, cursor uint64, limit int) ([]*Friend, error)
	// FindPendingByRecipientID 收件箱列表：caller 作为目标方收到的 PENDING 申请
	FindPendingByRecipientID(ctx context.Context, recipientID string, cursor uint64, limit int) ([]*Friend, error)
	// CountPendingByRecipientID 收件箱计数
	CountPendingByRecipientID(ctx context.Context, recipientID string) (uint64, error)
	// UpdateStatusIfRecipient 带鉴权 + 状态条件的一次性 UPDATE。
	// 命中 0 行折叠 ErrFriendNotFound，避免 TOCTOU 和越权读。
	//
	// 注意：没有「只按主键改状态」的无鉴权版本 —— 那种接口一旦被误用
	// 就会把已经 rejected 的申请改回 accepted。
	UpdateStatusIfRecipient(ctx context.Context, id uint64, recipientID string, from, to FriendStatus) (*Friend, error)
	// GetRecipientAndStatus 鉴权失败后的最小探测，只读 friend_id + status 两列
	GetRecipientAndStatus(ctx context.Context, id uint64) (recipientID string, status FriendStatus, err error)
	// DeleteByPair 按正向 (user_id, friend_id) 删，找不到返 ErrFriendNotFound
	DeleteByPair(ctx context.Context, userID, friendID string) error
	// DeleteByEitherPair 双向查 + 删 包同一事务
	DeleteByEitherPair(ctx context.Context, callerID, otherID string) (deleted bool, err error)
}

// FriendLimiter 好友域频控（Redis token bucket），命中上限返 ErrFriendRateLimited
type FriendLimiter interface {
	AllowApply(ctx context.Context, callerID string) error
	AllowAcceptReject(ctx context.Context, callerID string) error
	AllowRemove(ctx context.Context, callerID string) error
}

// noopFriendLimiter limiter 为 nil 时的替身，全部放行。
type noopFriendLimiter struct{}

func (noopFriendLimiter) AllowApply(context.Context, string) error        { return nil }
func (noopFriendLimiter) AllowAcceptReject(context.Context, string) error { return nil }
func (noopFriendLimiter) AllowRemove(context.Context, string) error       { return nil }

var (
	ErrFriendNotFound        = kratoserrors.NotFound(v1.ErrorReason_FRIEND_NOT_FOUND.String(), "friend not found")
	ErrFriendInvalidArgument = kratoserrors.BadRequest(v1.ErrorReason_FRIEND_INVALID_ARGUMENT.String(), "invalid friend argument")
	ErrFriendAlreadyFriends  = kratoserrors.Conflict(v1.ErrorReason_FRIEND_ALREADY_FRIENDS.String(), "already friends")
	ErrFriendAlreadyApplied  = kratoserrors.Conflict(v1.ErrorReason_FRIEND_ALREADY_APPLIED.String(), "friend request already pending")
	ErrFriendPendingNotFound = kratoserrors.NotFound(v1.ErrorReason_FRIEND_PENDING_NOT_FOUND.String(), "pending friend request not found")
	ErrFriendNotRecipient    = kratoserrors.Forbidden(v1.ErrorReason_FRIEND_NOT_RECIPIENT.String(), "caller is not the recipient of this friend request")
	ErrFriendSelf            = kratoserrors.BadRequest(v1.ErrorReason_FRIEND_SELF.String(), "cannot add yourself as a friend")
	ErrFriendRateLimited     = kratoserrors.New(429, v1.ErrorReason_FRIEND_RATE_LIMITED.String(), "too many requests")
	// ErrFriendInternal 表示 DI 失败等内部配置错误（500），不是 caller 输入错
	ErrFriendInternal = kratoserrors.New(500, "FRIEND_INTERNAL", "friend internal error")
)

// FriendUsecase 好友用例编排
type FriendUsecase struct {
	repo     FriendRepo
	profiles FriendProfileFetcher
	limiter  FriendLimiter
	logger   *slog.Logger
}

// NewFriendUsecase 构造 FriendUsecase。
// profiles 为 nil 时走降级，limiter 为 nil 时关闭限频，logger 为 nil 走 slog.Default。
func NewFriendUsecase(repo FriendRepo, profiles FriendProfileFetcher, limiter FriendLimiter, logger *slog.Logger) *FriendUsecase {
	if logger == nil {
		logger = slog.Default()
	}
	if limiter == nil {
		limiter = noopFriendLimiter{}
	}
	return &FriendUsecase{repo: repo, profiles: profiles, limiter: limiter, logger: logger}
}

// Apply 发起好友申请。callerID == targetID 拒绝；已是好友 / 已 PENDING 折叠相应错误；
// 反向 PENDING 自动收尾成 ACCEPTED。message 超过 200 字符截断并 trim 空格。
func (uc *FriendUsecase) Apply(ctx context.Context, callerID, targetID string, message string) (*FriendApplyResult, error) {
	if uc.repo == nil {
		return nil, ErrFriendInvalidArgument
	}
	if callerID == "" || targetID == "" {
		return nil, ErrFriendInvalidArgument
	}
	if callerID == targetID {
		return nil, ErrFriendSelf
	}
	if err := uc.limiter.AllowApply(ctx, callerID); err != nil {
		return nil, ErrFriendRateLimited
	}

	// 二次防御裁剪 + 去两边空格
	message = strings.TrimSpace(truncateRunes(message, MaxApplyMessageLen))

	forward, reverse, err := uc.repo.FindBothDirections(ctx, callerID, targetID)
	if err != nil {
		return nil, err
	}

	// 1. 正向处理: ACCEPTED/PENDING 直接报错；REJECTED 删除后走新建
	if forward != nil {
		switch forward.Status {
		case FriendStatusAccepted:
			return nil, ErrFriendAlreadyFriends
		case FriendStatusPending:
			return nil, ErrFriendAlreadyApplied
		case FriendStatusRejected:
			if err := uc.repo.DeleteByPair(ctx, callerID, targetID); err != nil {
				return nil, err
			}
		}
	}

	// 2. 反向处理: PENDING 自动收尾成 ACCEPTED；ACCEPTED 已是好友；REJECTED 不允许翻盘
	if reverse != nil {
		if reverse.Status == FriendStatusAccepted {
			return nil, ErrFriendAlreadyFriends
		}
		if reverse.Status == FriendStatusPending {
			// 用带状态守卫的版本：并发下对方可能已经 Accept/Reject 了这条申请，
			// 无守卫的 UpdateStatus 会把已经 rejected 的记录改回 accepted。
			accepted, err := uc.repo.UpdateStatusIfRecipient(ctx, reverse.ID, callerID, FriendStatusPending, FriendStatusAccepted)
			if err != nil {
				if errors.Is(err, ErrFriendNotFound) {
					// 并发被别人抢先处理了 —— 重读一次看现在是什么状态。
					return nil, ErrFriendAlreadyFriends
				}
				return nil, err
			}
			return &FriendApplyResult{RequestID: accepted.ID, Status: accepted.Status}, nil
		}
		// REJECTED 反向不允许翻盘，由对方重新发
		return nil, ErrFriendPendingNotFound
	}

	// 3. 新建 PENDING
	id, err := uc.repo.Save(ctx, &Friend{
		UserID: callerID, FriendID: targetID, Status: FriendStatusPending, Message: message,
	})
	if err != nil {
		if errors.Is(err, ErrFriendAlreadyApplied) {
			// 撞唯一索引 —— 说明并发下已经有人建了这条「两人对」。
			// 可能是同向（同一个人申请两次），也可能是反向
			// （对方同时也在申请我）。反向的话应该自动收尾成好友，
			// 而不是报「已申请」。
			return uc.resolveApplyConflict(ctx, callerID, targetID, 3)
		}
		return nil, err
	}
	return &FriendApplyResult{RequestID: id, Status: FriendStatusPending}, nil
}

// resolveApplyConflict 处理 Save 撞唯一索引的情况。
//
// 并发下（a→b 与 b→a 同时发起）唯一索引挡下了第二条写入，
// 但那不代表「已申请」—— 反向的那条 PENDING 应该收尾成好友。
//
// retries 限制重试次数：每次重试都是一次状态快照，
// 并发下状态可能一直在变，无限重试会变成活锁。
func (uc *FriendUsecase) resolveApplyConflict(ctx context.Context, callerID, targetID string, retries int) (*FriendApplyResult, error) {
	forward, reverse, err := uc.repo.FindBothDirections(ctx, callerID, targetID)
	if err != nil {
		return nil, err
	}
	// 反向 PENDING：对方也在申请我 —— 收尾成好友。
	if reverse != nil && reverse.Status == FriendStatusPending {
		accepted, err := uc.repo.UpdateStatusIfRecipient(ctx, reverse.ID, callerID, FriendStatusPending, FriendStatusAccepted)
		if err == nil {
			return &FriendApplyResult{RequestID: accepted.ID, Status: accepted.Status}, nil
		}
		if !errors.Is(err, ErrFriendNotFound) {
			return nil, err
		}
		// 并发下又被别人处理了 —— 重新看一眼当前状态。
		if retries > 0 {
			return uc.resolveApplyConflict(ctx, callerID, targetID, retries-1)
		}
		return nil, ErrFriendAlreadyApplied
	}
	// 同向已经有记录了
	if forward != nil {
		switch forward.Status {
		case FriendStatusAccepted:
			return nil, ErrFriendAlreadyFriends
		case FriendStatusPending:
			return nil, ErrFriendAlreadyApplied
		}
	}
	if reverse != nil && reverse.Status == FriendStatusAccepted {
		return nil, ErrFriendAlreadyFriends
	}
	return nil, ErrFriendAlreadyApplied
}

// Accept 接受申请。caller 必须是 PENDING 的目标方 (friend_id == callerID)，
// 否则折叠 PENDING_NOT_FOUND / NOT_RECIPIENT。
func (uc *FriendUsecase) Accept(ctx context.Context, callerID string, requestID uint64) (*Friend, error) {
	if uc.repo == nil {
		return nil, ErrFriendInvalidArgument
	}
	if callerID == "" || requestID == 0 {
		return nil, ErrFriendInvalidArgument
	}
	if err := uc.limiter.AllowAcceptReject(ctx, callerID); err != nil {
		return nil, ErrFriendRateLimited
	}

	updated, err := uc.repo.UpdateStatusIfRecipient(ctx, requestID, callerID, FriendStatusPending, FriendStatusAccepted)
	if err == nil {
		// 归一化「对方」到 FriendID：caller 是接收方，对方是发起方
		return normalizePeer(updated, callerID), nil
	}
	if !errors.Is(err, ErrFriendNotFound) {
		return nil, err
	}
	// 主路径 0 行 → 用最小探测区分 NOT_RECIPIENT / PENDING_NOT_FOUND
	recipientID, status, err := uc.repo.GetRecipientAndStatus(ctx, requestID)
	if err != nil {
		if errors.Is(err, ErrFriendNotFound) {
			return nil, ErrFriendPendingNotFound
		}
		return nil, err
	}
	if recipientID != callerID {
		return nil, ErrFriendNotRecipient
	}
	if status != FriendStatusPending {
		return nil, ErrFriendPendingNotFound
	}
	// 极小竞态：探测时仍 PENDING，但下一条 UPDATE 仍会因并发 Accept/Reject 而 0 行
	return nil, ErrFriendPendingNotFound
}

// Reject 拒绝申请，鉴权同 Accept
func (uc *FriendUsecase) Reject(ctx context.Context, callerID string, requestID uint64) error {
	if uc.repo == nil {
		return ErrFriendInternal
	}
	if callerID == "" || requestID == 0 {
		return ErrFriendInvalidArgument
	}
	if err := uc.limiter.AllowAcceptReject(ctx, callerID); err != nil {
		return ErrFriendRateLimited
	}

	_, err := uc.repo.UpdateStatusIfRecipient(ctx, requestID, callerID, FriendStatusPending, FriendStatusRejected)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrFriendNotFound) {
		return err
	}
	// 主路径 0 行 → 用最小探测区分 NOT_RECIPIENT / PENDING_NOT_FOUND
	recipientID, status, err := uc.repo.GetRecipientAndStatus(ctx, requestID)
	if err != nil {
		if errors.Is(err, ErrFriendNotFound) {
			return ErrFriendPendingNotFound
		}
		return err
	}
	if recipientID != callerID {
		return ErrFriendNotRecipient
	}
	if status != FriendStatusPending {
		return ErrFriendPendingNotFound
	}
	return ErrFriendPendingNotFound
}

// Remove 删好友，任意一方调用皆可
func (uc *FriendUsecase) Remove(ctx context.Context, callerID, friendID string) error {
	if uc.repo == nil {
		return ErrFriendInternal
	}
	if callerID == "" || friendID == "" {
		return ErrFriendInvalidArgument
	}
	if callerID == friendID {
		return ErrFriendInvalidArgument
	}
	if err := uc.limiter.AllowRemove(ctx, callerID); err != nil {
		return ErrFriendRateLimited
	}

	deleted, err := uc.repo.DeleteByEitherPair(ctx, callerID, friendID)
	if err != nil {
		return err
	}
	if !deleted {
		return ErrFriendNotFound
	}
	return nil
}

// List 列出 caller 的所有 ACCEPTED 好友，cursor 分页。
// 双向语义：好友关系只存单条 record，caller 可能命中 user_id 列也可能命中 friend_id 列，
// 两种都要出现在列表里，这里把每条记录的「对方」归一化到 FriendID。
// limit <= 0 走默认 50，末位 ID 即 next_cursor。
func (uc *FriendUsecase) List(ctx context.Context, callerID string, cursor uint64, limit int) ([]*Friend, uint64, error) {
	if uc.repo == nil {
		return nil, 0, ErrFriendInternal
	}
	if callerID == "" {
		return nil, 0, ErrFriendInvalidArgument
	}
	if limit <= 0 {
		limit = 50
	}

	accepted, err := uc.repo.FindAcceptedByUserIDAndID(ctx, callerID, cursor, limit)
	if err != nil {
		return nil, 0, err
	}

	var nextCursor uint64
	// len == limit 表示可能还有下一页，末位 ID 即 next_cursor；len < limit 表示已到尾页
	if len(accepted) == limit && limit > 0 {
		nextCursor = accepted[len(accepted)-1].ID
	}

	// 归一化「对方」到 FriendID。副本返回，避免改到 repo 持有的对象
	out := make([]*Friend, 0, len(accepted))
	for _, f := range accepted {
		if f == nil {
			continue
		}
		out = append(out, normalizePeer(f, callerID))
	}

	// 跨服务拉资料失败只降级（好友列表主体照常返回），但必须留日志
	if err := fillProfiles(ctx, uc.profiles, callerID, out); err != nil {
		uc.logger.WarnContext(ctx, "好友资料补全降级", "err", err, "caller_id", callerID, "count", len(out))
	}
	return out, nextCursor, nil
}

// normalizePeer 把记录的「对方」归一化到 FriendID，返回副本（不改原对象）。
// 好友关系只存单条 record (user_id=发起方, friend_id=接收方)，caller 落在
// 哪一列取决于谁先发起，所以对 caller 而言「对方」是另一列。
func normalizePeer(f *Friend, callerID string) *Friend {
	if f == nil {
		return nil
	}
	c := *f
	if c.UserID == callerID {
		c.FriendID = f.FriendID
	} else {
		c.FriendID = f.UserID
	}
	return &c
}

// ListPendingRequests 收件箱列表——caller 作为目标方收到的 PENDING 申请。
// 返回的 Friend.FriendID 这里指「申请发起方」，service 层照常填到 FriendInfo.friend_id。
// limit <= 0 走默认 50。
func (uc *FriendUsecase) ListPendingRequests(ctx context.Context, callerID string, cursor uint64, limit int) ([]*Friend, uint64, error) {
	if uc.repo == nil {
		return nil, 0, ErrFriendInvalidArgument
	}
	if callerID == "" {
		return nil, 0, ErrFriendInvalidArgument
	}
	if limit <= 0 {
		limit = 50
	}

	all, err := uc.repo.FindPendingByRecipientID(ctx, callerID, cursor, limit+1)
	if err != nil {
		return nil, 0, err
	}
	out := make([]*Friend, 0, len(all))
	for _, f := range all {
		// 这些记录 friend_id == callerID，「对方」是发起方
		out = append(out, normalizePeer(f, callerID))
		if len(out) >= limit {
			break
		}
	}
	var nextCursor uint64
	// 游标要取**本页最后一条返回的记录**的 id（out[len(out)-1]），
	// 不是 all[len(all)-1] —— 后者是多取出来的那条哨兵记录，不在响应里，
	// 拿它当游标会让它永远被跳过（下一页从 id > 哨兵id 开始）。
	if len(all) > limit && len(out) > 0 {
		nextCursor = out[len(out)-1].ID
	}
	if err := fillProfiles(ctx, uc.profiles, callerID, out); err != nil {
		uc.logger.WarnContext(ctx, "好友资料补全降级", "err", err, "caller_id", callerID, "count", len(out))
	}
	return out, nextCursor, nil
}

// CountPending caller 作为目标方收到的 PENDING 申请总数
func (uc *FriendUsecase) CountPending(ctx context.Context, callerID string) (uint64, error) {
	if uc.repo == nil {
		return 0, ErrFriendInvalidArgument
	}
	if callerID == "" {
		return 0, ErrFriendInvalidArgument
	}
	return uc.repo.CountPendingByRecipientID(ctx, callerID)
}

// fillProfiles 按 friend_id 把 profile 写回 friend 列表，原地写入 Profile 字段。
// 整批 err 降级；单条缺省静默。
func fillProfiles(ctx context.Context, fetcher FriendProfileFetcher, callerID string, friends []*Friend) error {
	if fetcher == nil || len(friends) == 0 {
		return nil
	}
	ids := make([]string, 0, len(friends))
	seen := make(map[string]struct{}, len(friends))
	for _, f := range friends {
		if _, ok := seen[f.FriendID]; ok {
			continue
		}
		seen[f.FriendID] = struct{}{}
		ids = append(ids, f.FriendID)
	}
	if len(ids) == 0 {
		return nil
	}
	profiles, err := fetcher.FetchProfiles(ctx, callerID, ids)
	if err != nil {
		return err
	}
	byID := make(map[string]*FriendProfile, len(profiles))
	for _, p := range profiles {
		byID[p.UserID] = p
	}
	for _, f := range friends {
		if p, ok := byID[f.FriendID]; ok {
			f.Profile = p
		}
	}
	return nil
}
