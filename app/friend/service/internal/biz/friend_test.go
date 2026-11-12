package biz

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// fakeFriendRepo 是 biz.FriendRepo 的内存替身。内部用 (userID, friendID) 二元组作唯一键，与 schema 的 uk_user_friend
// 对齐。Save 命中重复键时返 ErrFriendAlreadyApplied（与 data 层将 ent
// 唯一冲突折叠成的错误一致）。
type fakeFriendRepo struct {
	// key = encodePair(userID, friendID)
	byPair map[string]*Friend
	byID   map[uint64]*Friend
	nextID uint64
}

func newFakeFriendRepo() *fakeFriendRepo {
	return &fakeFriendRepo{
		byPair: make(map[string]*Friend),
		byID:   make(map[uint64]*Friend),
		nextID: 1,
	}
}

func encodePair(userID, friendID string) string {
	// 用 "\x00" 分隔不会与合法 id 冲突 (id 不会包含空字节)
	return userID + "\x00" + friendID
}

func uint64ToStr(n uint64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func (r *fakeFriendRepo) Save(_ context.Context, f *Friend) (uint64, error) {
	key := encodePair(f.UserID, f.FriendID)
	if _, exists := r.byPair[key]; exists {
		return 0, ErrFriendAlreadyApplied
	}
	// 取当前 max id + 1, 避免删除记录后 nextID 回退导致 id 复用
	maxID := r.nextID
	for id := range r.byID {
		if id >= maxID {
			maxID = id + 1
		}
	}
	f.ID = maxID
	r.nextID = maxID + 1
	r.byPair[key] = f
	r.byID[f.ID] = f
	return f.ID, nil
}

func (r *fakeFriendRepo) FindByID(_ context.Context, id uint64) (*Friend, error) {
	f, ok := r.byID[id]
	if !ok {
		return nil, ErrFriendNotFound
	}
	return f, nil
}

func (r *fakeFriendRepo) FindByPair(_ context.Context, userID, friendID string) (*Friend, error) {
	f, ok := r.byPair[encodePair(userID, friendID)]
	if !ok {
		return nil, ErrFriendNotFound
	}
	return f, nil
}

func (r *fakeFriendRepo) FindBothDirections(_ context.Context, a, b string) (*Friend, *Friend, error) {
	forward, fok := r.byPair[encodePair(a, b)]
	reverse, rok := r.byPair[encodePair(b, a)]
	if !fok && !rok {
		return nil, nil, nil
	}
	return forward, reverse, nil
}

// FindAcceptedByUserIDAndID 双向命中：caller 既可能在 user_id 也可能在 friend_id 上，
// 与 data 层 friend.Or(UserIDEQ, FriendIDEQ) 的语义保持一致。
func (r *fakeFriendRepo) FindAcceptedByUserIDAndID(_ context.Context, userID string, cursor uint64, limit int) ([]*Friend, error) {
	var out []*Friend
	for _, f := range r.byID {
		if f.UserID != userID && f.FriendID != userID {
			continue
		}
		if f.Status != FriendStatusAccepted {
			continue
		}
		if cursor > 0 && f.ID <= cursor {
			continue
		}
		out = append(out, f)
	}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].ID < out[i].ID {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *fakeFriendRepo) UpdateStatusIfRecipient(_ context.Context, id uint64, recipientID string, from, to FriendStatus) (*Friend, error) {
	f, ok := r.byID[id]
	if !ok {
		return nil, ErrFriendNotFound
	}
	if f.FriendID != recipientID || f.Status != from {
		return nil, ErrFriendNotFound
	}
	f.Status = to
	return f, nil
}

func (r *fakeFriendRepo) GetRecipientAndStatus(_ context.Context, id uint64) (string, FriendStatus, error) {
	f, ok := r.byID[id]
	if !ok {
		return "", 0, ErrFriendNotFound
	}
	return f.FriendID, f.Status, nil
}

func (r *fakeFriendRepo) DeleteByPair(_ context.Context, userID, friendID string) error {
	key := encodePair(userID, friendID)
	f, ok := r.byPair[key]
	if !ok {
		return ErrFriendNotFound
	}
	delete(r.byPair, key)
	delete(r.byID, f.ID)
	return nil
}

// FindPendingByRecipientID 是 F4 ListPendingRequests 的内存替身。
// 仅返回 friend_id == recipientID 且 status == PENDING 的记录, 按 id 升序。
// cursor 语义与 FindAcceptedByUserIDAndID 相同。
func (r *fakeFriendRepo) FindPendingByRecipientID(_ context.Context, recipientID string, cursor uint64, limit int) ([]*Friend, error) {
	var out []*Friend
	for _, f := range r.byID {
		if f.FriendID != recipientID {
			continue
		}
		if f.Status != FriendStatusPending {
			continue
		}
		if cursor > 0 && f.ID <= cursor {
			continue
		}
		out = append(out, f)
	}
	// 排序: id 升序
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].ID < out[i].ID {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// CountPendingByRecipientID 是 F4 CountPending 的内存替身。
func (r *fakeFriendRepo) CountPendingByRecipientID(_ context.Context, recipientID string) (uint64, error) {
	var n uint64
	for _, f := range r.byID {
		if f.FriendID == recipientID && f.Status == FriendStatusPending {
			n++
		}
	}
	return n, nil
}

// DeleteByEitherPair F6: 内存替身, 模拟 data 层"双向探 + 删"的事务语义。
// 单元测试不需要真事务: 单线程顺序执行即可。
func (r *fakeFriendRepo) DeleteByEitherPair(_ context.Context, callerID, otherID string) (bool, error) {
	// 正向: callerID=user, otherID=friend
	if f, ok := r.byPair[encodePair(callerID, otherID)]; ok {
		if f.Status != FriendStatusAccepted {
			return false, nil
		}
		delete(r.byPair, encodePair(callerID, otherID))
		delete(r.byID, f.ID)
		return true, nil
	}
	// 反向
	if f, ok := r.byPair[encodePair(otherID, callerID)]; ok {
		if f.Status != FriendStatusAccepted {
			return false, nil
		}
		delete(r.byPair, encodePair(otherID, callerID))
		delete(r.byID, f.ID)
		return true, nil
	}
	return false, nil
}

// fakeFriendLimiter 是 biz.FriendLimiter 的内存替身, 可在测试里按需
// 触发 ErrFriendRateLimited。
//
// 默认零值表现: 三条路径都返 nil (放行)。需要命中频控时设对应字段。
type fakeFriendLimiter struct {
	rejectApply        bool
	rejectAcceptReject bool
	rejectRemove       bool
}

func (l *fakeFriendLimiter) AllowApply(_ context.Context, _ string) error {
	if l.rejectApply {
		return ErrFriendRateLimited
	}
	return nil
}

func (l *fakeFriendLimiter) AllowAcceptReject(_ context.Context, _ string) error {
	if l.rejectAcceptReject {
		return ErrFriendRateLimited
	}
	return nil
}

func (l *fakeFriendLimiter) AllowRemove(_ context.Context, _ string) error {
	if l.rejectRemove {
		return ErrFriendRateLimited
	}
	return nil
}

// fakeFriendFetcher 是 biz.FriendProfileFetcher 的内存替身，按 id 生成固定 profile。
// 需要触发 user service 不可达时设 failFetch=true。
type fakeFriendFetcher struct {
	failFetch bool
}

func (f *fakeFriendFetcher) FetchProfiles(_ context.Context, _ string, ids []string) ([]*FriendProfile, error) {
	if f.failFetch {
		return nil, errors.New("fake user service unavailable")
	}
	out := make([]*FriendProfile, 0, len(ids))
	for _, id := range ids {
		out = append(out, &FriendProfile{
			UserID:   id,
			Nickname: "user-" + id,
			Avatar:   "https://example.com/avatar/" + id,
		})
	}
	return out, nil
}
func TestFriendUsecase_Apply_Basic(t *testing.T) {
	repo := newFakeFriendRepo()
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, &fakeFriendLimiter{}, nil)

	res, err := uc.Apply(context.Background(), "uid-1", "uid-2", "hi")
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if res.Status != FriendStatusPending {
		t.Fatalf("expected PENDING, got %v", res.Status)
	}
	if res.RequestID == 0 {
		t.Fatalf("expected non-zero request id")
	}
}

func TestFriendUsecase_Apply_PersistsMessage(t *testing.T) {
	repo := newFakeFriendRepo()
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, &fakeFriendLimiter{}, nil)

	res, err := uc.Apply(context.Background(), "uid-1", "uid-2", "hello")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Status != FriendStatusPending {
		t.Fatalf("expected PENDING, got %v", res.Status)
	}
	stored, err := repo.FindByID(context.Background(), res.RequestID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if stored.Message != "hello" {
		t.Fatalf("Message = %q, want hello", stored.Message)
	}
}

func TestFriendUsecase_Apply_MessageOver200Truncated(t *testing.T) {
	repo := newFakeFriendRepo()
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, &fakeFriendLimiter{}, nil)

	long := strings.Repeat("a", 500)
	res, err := uc.Apply(context.Background(), "uid-1", "uid-2", long)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	stored, err := repo.FindByID(context.Background(), res.RequestID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if len(stored.Message) != 200 {
		t.Fatalf("Message length = %d, want 200", len(stored.Message))
	}
}

func TestFriendUsecase_Apply_EmptyMessageStored(t *testing.T) {
	repo := newFakeFriendRepo()
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, &fakeFriendLimiter{}, nil)

	res, err := uc.Apply(context.Background(), "uid-1", "uid-2", "")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	stored, err := repo.FindByID(context.Background(), res.RequestID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if stored.Message != "" {
		t.Fatalf("Message = %q, want empty", stored.Message)
	}
}

func TestFriendUsecase_Apply_SelfRejected(t *testing.T) {
	repo := newFakeFriendRepo()
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, &fakeFriendLimiter{}, nil)

	_, err := uc.Apply(context.Background(), "uid-1", "uid-1", "")
	if !errors.Is(err, ErrFriendSelf) {
		t.Fatalf("expected ErrFriendSelf, got %v", err)
	}
}

func TestFriendUsecase_Apply_EmptyUID(t *testing.T) {
	repo := newFakeFriendRepo()
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, &fakeFriendLimiter{}, nil)

	if _, err := uc.Apply(context.Background(), "", "uid-2", ""); !errors.Is(err, ErrFriendInvalidArgument) {
		t.Fatalf("caller empty expected ErrFriendInvalidArgument, got %v", err)
	}
	if _, err := uc.Apply(context.Background(), "uid-1", "", ""); !errors.Is(err, ErrFriendInvalidArgument) {
		t.Fatalf("target empty expected ErrFriendInvalidArgument, got %v", err)
	}
}

func TestFriendUsecase_Apply_AlreadyAccepted(t *testing.T) {
	repo := newFakeFriendRepo()
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, &fakeFriendLimiter{}, nil)

	// 1 -> 2 PENDING
	res, err := uc.Apply(context.Background(), "uid-1", "uid-2", "")
	if err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	// 2 接受
	if _, err := uc.Accept(context.Background(), "uid-2", res.RequestID); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	// 1 再次 Apply → 已是好友
	_, err = uc.Apply(context.Background(), "uid-1", "uid-2", "")
	if !errors.Is(err, ErrFriendAlreadyFriends) {
		t.Fatalf("expected ErrFriendAlreadyFriends, got %v", err)
	}
}

func TestFriendUsecase_Apply_AlreadyPending(t *testing.T) {
	repo := newFakeFriendRepo()
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, &fakeFriendLimiter{}, nil)

	if _, err := uc.Apply(context.Background(), "uid-1", "uid-2", ""); err != nil {
		t.Fatalf("first: %v", err)
	}
	_, err := uc.Apply(context.Background(), "uid-1", "uid-2", "")
	if !errors.Is(err, ErrFriendAlreadyApplied) {
		t.Fatalf("expected ErrFriendAlreadyApplied, got %v", err)
	}
}

func TestFriendUsecase_Apply_ReversePendingAutoAccept(t *testing.T) {
	repo := newFakeFriendRepo()
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, &fakeFriendLimiter{}, nil)

	// 2 -> 1 PENDING
	res1, err := uc.Apply(context.Background(), "uid-2", "uid-1", "")
	if err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	// 1 -> 2: 反向 PENDING, 应自动 ACCEPTED
	res2, err := uc.Apply(context.Background(), "uid-1", "uid-2", "")
	if err != nil {
		t.Fatalf("reverse Apply: %v", err)
	}
	if res2.Status != FriendStatusAccepted {
		t.Fatalf("expected ACCEPTED, got %v", res2.Status)
	}
	// 应该是同一条 record, id 一致
	if res1.RequestID != res2.RequestID {
		t.Fatalf("expected same record id, got %d vs %d", res1.RequestID, res2.RequestID)
	}
}

func TestFriendUsecase_Apply_AfterRejectRebuilds(t *testing.T) {
	repo := newFakeFriendRepo()
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, &fakeFriendLimiter{}, nil)

	// 1 -> 2 PENDING → 2 reject
	if _, err := uc.Apply(context.Background(), "uid-1", "uid-2", ""); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := uc.Reject(context.Background(), "uid-2", 1); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	// 1 再 Apply → 删除旧 REJECTED, 走新建
	res, err := uc.Apply(context.Background(), "uid-1", "uid-2", "")
	if err != nil {
		t.Fatalf("re-Apply: %v", err)
	}
	if res.Status != FriendStatusPending {
		t.Fatalf("expected new PENDING, got %v", res.Status)
	}
}

func TestFriendUsecase_Accept_NotRecipient(t *testing.T) {
	repo := newFakeFriendRepo()
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, &fakeFriendLimiter{}, nil)

	// 1 -> 2 PENDING
	res, err := uc.Apply(context.Background(), "uid-1", "uid-2", "")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// 3 不是 target, 不能 Accept
	_, err = uc.Accept(context.Background(), "uid-3", res.RequestID)
	if !errors.Is(err, ErrFriendNotRecipient) {
		t.Fatalf("expected ErrFriendNotRecipient, got %v", err)
	}
}

func TestFriendUsecase_Accept_AlreadyAccepted(t *testing.T) {
	repo := newFakeFriendRepo()
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, &fakeFriendLimiter{}, nil)

	res, err := uc.Apply(context.Background(), "uid-1", "uid-2", "")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := uc.Accept(context.Background(), "uid-2", res.RequestID); err != nil {
		t.Fatalf("Accept 1: %v", err)
	}
	// 二次 Accept
	_, err = uc.Accept(context.Background(), "uid-2", res.RequestID)
	if !errors.Is(err, ErrFriendPendingNotFound) {
		t.Fatalf("expected ErrFriendPendingNotFound, got %v", err)
	}
}

func TestFriendUsecase_Reject_HappyPath(t *testing.T) {
	repo := newFakeFriendRepo()
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, &fakeFriendLimiter{}, nil)

	res, err := uc.Apply(context.Background(), "uid-1", "uid-2", "")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := uc.Reject(context.Background(), "uid-2", res.RequestID); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	// 验证: 通过 list 查不到 ACCEPTED
	friends, _, err := uc.List(context.Background(), "uid-1", 0, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(friends) != 0 {
		t.Fatalf("expected empty list after reject, got %d", len(friends))
	}
}

func TestFriendUsecase_Remove_BothDirections(t *testing.T) {
	repo := newFakeFriendRepo()
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, &fakeFriendLimiter{}, nil)

	// 1 -> 2 accepted
	res, err := uc.Apply(context.Background(), "uid-1", "uid-2", "")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := uc.Accept(context.Background(), "uid-2", res.RequestID); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	// 1 删 2 (正向)
	if err := uc.Remove(context.Background(), "uid-1", "uid-2"); err != nil {
		t.Fatalf("Remove by initiator: %v", err)
	}
	// 记录已删
	err = uc.Remove(context.Background(), "uid-1", "uid-2")
	if !errors.Is(err, ErrFriendNotFound) {
		t.Fatalf("expected ErrFriendNotFound, got %v", err)
	}

	// 重新建, 这次 2 删 1 (反向)
	res, err = uc.Apply(context.Background(), "uid-1", "uid-2", "")
	if err != nil {
		t.Fatalf("re-Apply: %v", err)
	}
	if _, err := uc.Accept(context.Background(), "uid-2", res.RequestID); err != nil {
		t.Fatalf("re-Accept: %v", err)
	}
	if err := uc.Remove(context.Background(), "uid-2", "uid-1"); err != nil {
		t.Fatalf("Remove by recipient: %v", err)
	}
}

func TestFriendUsecase_Remove_OnlyPending(t *testing.T) {
	repo := newFakeFriendRepo()
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, &fakeFriendLimiter{}, nil)

	// PENDING 不允许 Remove
	if _, err := uc.Apply(context.Background(), "uid-1", "uid-2", ""); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := uc.Remove(context.Background(), "uid-1", "uid-2"); !errors.Is(err, ErrFriendNotFound) {
		t.Fatalf("expected ErrFriendNotFound for PENDING, got %v", err)
	}
}

func TestFriendUsecase_List_OnlyAccepted(t *testing.T) {
	repo := newFakeFriendRepo()
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, &fakeFriendLimiter{}, nil)

	// 1->2 PENDING (不计入列表)
	if _, err := uc.Apply(context.Background(), "uid-1", "uid-2", ""); err != nil {
		t.Fatalf("Apply 1->2: %v", err)
	}
	// 1->3 已 accepted
	res3, err := uc.Apply(context.Background(), "uid-1", "uid-3", "")
	if err != nil {
		t.Fatalf("Apply 1->3: %v", err)
	}
	if _, err := uc.Accept(context.Background(), "uid-3", res3.RequestID); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	// 1->4 REJECTED
	res4, err := uc.Apply(context.Background(), "uid-1", "uid-4", "")
	if err != nil {
		t.Fatalf("Apply 1->4: %v", err)
	}
	if err := uc.Reject(context.Background(), "uid-4", res4.RequestID); err != nil {
		t.Fatalf("Reject: %v", err)
	}

	friends, _, err := uc.List(context.Background(), "uid-1", 0, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(friends) != 1 {
		t.Fatalf("expected 1 friend (uid 3), got %d", len(friends))
	}
	if friends[0].FriendID != "uid-3" {
		t.Fatalf("expected friend_id=uid-3, got %s", friends[0].FriendID)
	}
}

func TestFriendUsecase_List_Pagination(t *testing.T) {
	repo := newFakeFriendRepo()
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, &fakeFriendLimiter{}, nil)

	// 建 5 个 accepted 关系
	for i := 2; i <= 6; i++ {
		uid := fmt.Sprintf("uid-%d", i)
		res, err := uc.Apply(context.Background(), "uid-1", uid, "")
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if _, err := uc.Accept(context.Background(), uid, res.RequestID); err != nil {
			t.Fatalf("Accept: %v", err)
		}
	}

	// 拿第一页 (limit=2)
	page1, next1, err := uc.List(context.Background(), "uid-1", 0, 2)
	if err != nil {
		t.Fatalf("List 1: %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("expected 2, got %d", len(page1))
	}
	if next1 == 0 {
		t.Fatalf("expected non-zero next cursor")
	}

	// 拿第二页
	page2, next2, err := uc.List(context.Background(), "uid-1", next1, 2)
	if err != nil {
		t.Fatalf("List 2: %v", err)
	}
	if len(page2) != 2 {
		t.Fatalf("expected 2, got %d", len(page2))
	}

	// 第三页: next2 = 0 表示已到末页（page2 把 limit+1 条全拿了, 剩余 0 条）。
	// next2 != 0 时 page3 还可能剩 1 条。两种情况分别验证。
	if next2 != 0 {
		_, next3, err := uc.List(context.Background(), "uid-1", next2, 2)
		if err != nil {
			t.Fatalf("List 3: %v", err)
		}
		if next3 != 0 {
			t.Fatalf("expected next=0 (end), got %d", next3)
		}
	}
}

// F1: cursor 必须基于未过滤序列, 否则掺杂 PENDING/REJECTED 的
// 中间行被剔掉, 旧逻辑会让 next_cursor 跳过记录。建一条 PENDING
// 夹在 4 个 ACCEPTED 中间, 验证第一页/第二页均不丢、不重。
func TestFriendUsecase_List_CursorSkipsFilteredRows(t *testing.T) {
	repo := newFakeFriendRepo()
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, &fakeFriendLimiter{}, nil)

	// 4 个 ACCEPTED + 1 个 PENDING 夹在中间: caller=1, targetID=2..6
	// 全部提申请后, 仅接受 2 / 4 / 5 / 6, 让 target=3 保持 PENDING。
	for i := 2; i <= 6; i++ {
		uid := fmt.Sprintf("uid-%d", i)
		res, err := uc.Apply(context.Background(), "uid-1", uid, "")
		if err != nil {
			t.Fatalf("Apply(%d): %v", i, err)
		}
		if i == 3 {
			continue // 让 target=3 维持 PENDING
		}
		if _, err := uc.Accept(context.Background(), uid, res.RequestID); err != nil {
			t.Fatalf("Accept(%d): %v", i, err)
		}
	}

	// 第一页 limit=2: status=ACCEPTED 在 repo 层过滤, 应返回前两个 ACCEPTED (ID 2, 4), cursor=4。
	page1, next1, err := uc.List(context.Background(), "uid-1", 0, 2)
	if err != nil {
		t.Fatalf("List page1: %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("page1 len = %d, want 2", len(page1))
	}
	if next1 == 0 {
		t.Fatalf("page1 next cursor should be non-zero (还有下一页)")
	}

	// 第二页: cursor=4 之后 ACCEPTED 的 5/6 进入第二页。PENDING 行(ID=3)被 repo 层过滤掉,不再影响 cursor。
	page2, _, err := uc.List(context.Background(), "uid-1", next1, 2)
	if err != nil {
		t.Fatalf("List page2: %v", err)
	}
	if len(page2) != 2 {
		t.Fatalf("page2 len = %d, want 2", len(page2))
	}

	// 收集两页所有 friend_id, 不能重复也不能漏 ACCEPTED。
	seen := map[string]int{}
	for _, f := range append(page1, page2...) {
		seen[f.FriendID]++
	}
	for _, want := range []string{"uid-2", "uid-4", "uid-5", "uid-6"} {
		if seen[want] != 1 {
			t.Fatalf("ACCEPTED friend_id=%s seen %d times, want 1 (full set: %v)", want, seen[want], seen)
		}
	}
	if seen["uid-3"] != 0 {
		t.Fatalf("PENDING friend_id=uid-3 should not appear in accepted list")
	}
}

// TestFriendUsecase_Apply_RateLimited 验证 Apply 命中频控时直接返
// ErrFriendRateLimited, 不会去查 repo (rejection should short-circuit)。
func TestFriendUsecase_Apply_RateLimited(t *testing.T) {
	repo := newFakeFriendRepo()
	lim := &fakeFriendLimiter{rejectApply: true}
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, lim, nil)

	_, err := uc.Apply(context.Background(), "uid-1", "uid-2", "hi")
	if !errors.Is(err, ErrFriendRateLimited) {
		t.Fatalf("Apply rate-limited: got %v, want ErrFriendRateLimited", err)
	}
}

// TestFriendUsecase_Accept_RateLimited 验证 Accept 路径频控。
func TestFriendUsecase_Accept_RateLimited(t *testing.T) {
	repo := newFakeFriendRepo()
	lim := &fakeFriendLimiter{rejectAcceptReject: true}
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, lim, nil)

	_, err := uc.Accept(context.Background(), "uid-1", 999)
	if !errors.Is(err, ErrFriendRateLimited) {
		t.Fatalf("Accept rate-limited: got %v, want ErrFriendRateLimited", err)
	}
}

// TestFriendUsecase_Reject_RateLimited 验证 Reject 路径频控。
func TestFriendUsecase_Reject_RateLimited(t *testing.T) {
	repo := newFakeFriendRepo()
	lim := &fakeFriendLimiter{rejectAcceptReject: true}
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, lim, nil)

	err := uc.Reject(context.Background(), "uid-1", 999)
	if !errors.Is(err, ErrFriendRateLimited) {
		t.Fatalf("Reject rate-limited: got %v, want ErrFriendRateLimited", err)
	}
}

// TestFriendUsecase_Remove_RateLimited 验证 Remove 路径频控。
func TestFriendUsecase_Remove_RateLimited(t *testing.T) {
	repo := newFakeFriendRepo()
	lim := &fakeFriendLimiter{rejectRemove: true}
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, lim, nil)

	err := uc.Remove(context.Background(), "uid-1", "uid-2")
	if !errors.Is(err, ErrFriendRateLimited) {
		t.Fatalf("Remove rate-limited: got %v, want ErrFriendRateLimited", err)
	}
}

// TestFriendUsecase_LimiterNilFallsBack 验证 limiter nil 时走 noop, 不报错。
func TestFriendUsecase_LimiterNilFallsBack(t *testing.T) {
	repo := newFakeFriendRepo()
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, &fakeFriendLimiter{}, nil)
	// 连发 5 次 Apply 都应成功 (没有 limiter 阻拦)
	for i := 2; i <= 6; i++ {
		if _, err := uc.Apply(context.Background(), "uid-1", fmt.Sprintf("uid-%d", i), ""); err != nil {
			t.Fatalf("Apply(%d) with nil limiter: %v", i, err)
		}
	}
}

// TestFriendUsecase_ListPendingRequests 验证:
//   - 只返回 caller 作为目标方 (friend_id == callerID) 的 PENDING 行
//   - 排除 caller 自己发出去的 PENDING (自己发的是 user_id == callerID)
//   - 排除 ACCEPTED / REJECTED
//   - cursor 分页正确
func TestFriendUsecase_ListPendingRequests(t *testing.T) {
	repo := newFakeFriendRepo()
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, &fakeFriendLimiter{}, nil)

	insert := func(user, friend string, status FriendStatus) uint64 {
		id := repo.nextID
		repo.nextID = id + 1
		f := &Friend{ID: id, UserID: user, FriendID: friend, Status: status}
		repo.byPair[encodePair(user, friend)] = f
		repo.byID[id] = f
		return id
	}
	// caller=1 收到的 PENDING (应收件)
	insert("uid-2", "uid-1", FriendStatusPending)
	insert("uid-3", "uid-1", FriendStatusPending)
	// caller=1 收到的 ACCEPTED / REJECTED (不应收件)
	insert("uid-4", "uid-1", FriendStatusAccepted)
	insert("uid-6", "uid-1", FriendStatusRejected)
	// caller=1 自己发出去的 PENDING (不应收件)
	insert("uid-1", "uid-5", FriendStatusPending)
	// 给 caller=2 的 PENDING (跟 caller=1 无关, 不应收件)
	insert("uid-7", "uid-2", FriendStatusPending)

	got, _, err := uc.ListPendingRequests(context.Background(), "uid-1", 0, 50)
	if err != nil {
		t.Fatalf("ListPendingRequests: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2 (only PENDING from friends to caller)", len(got))
	}
	// friend_id 指向"申请发起方"（proto 注释: friend_id 字段在此指向申请发起方），
	// 即 2 和 3，而不是 caller 自己。
	wantSenders := map[string]bool{"uid-2": true, "uid-3": true}
	for _, f := range got {
		if !wantSenders[f.FriendID] {
			t.Errorf("row.friend_id = %s, want one of {uid-2,uid-3} (the applicant)", f.FriendID)
		}
		if !wantSenders[f.UserID] {
			t.Errorf("row.user_id = %s, want one of {uid-2,uid-3}", f.UserID)
		}
	}
}

// TestFriendUsecase_CountPending 验证 CountPending 只数 PENDING, 不数
// ACCEPTED / REJECTED; 只数 caller 作为目标方的。
func TestFriendUsecase_CountPending(t *testing.T) {
	repo := newFakeFriendRepo()
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, &fakeFriendLimiter{}, nil)

	insert := func(user, friend string, status FriendStatus) uint64 {
		id := repo.nextID
		repo.nextID = id + 1
		f := &Friend{ID: id, UserID: user, FriendID: friend, Status: status}
		repo.byPair[encodePair(user, friend)] = f
		repo.byID[id] = f
		return id
	}
	// 混入多条: caller=1 收到 2 PENDING, 1 ACCEPTED, 1 REJECTED;
	// caller=1 自己发出去的不算; 给 caller=2 的不算。
	insert("uid-2", "uid-1", FriendStatusPending)
	insert("uid-3", "uid-1", FriendStatusPending)
	insert("uid-4", "uid-1", FriendStatusAccepted)
	insert("uid-5", "uid-1", FriendStatusRejected)
	insert("uid-1", "uid-6", FriendStatusPending) // self-sent, 不算
	insert("uid-7", "uid-2", FriendStatusPending) // 给 2 的, 不算

	got, err := uc.CountPending(context.Background(), "uid-1")
	if err != nil {
		t.Fatalf("CountPending: %v", err)
	}
	if got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
}

// Accept 返回的 Friend.FriendID 必须是"对方"（发起方），不是 caller 自己。
func TestFriendUsecase_Accept_ReturnsPeerNotCaller(t *testing.T) {
	repo := newFakeFriendRepo()
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, &fakeFriendLimiter{}, nil)

	// 1 发起，2 接受
	res, err := uc.Apply(context.Background(), "uid-1", "uid-2", "")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	accepted, err := uc.Accept(context.Background(), "uid-2", res.RequestID)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if accepted.FriendID != "uid-1" {
		t.Fatalf("FriendID = %s, want uid-1 (the applicant), got %s", accepted.FriendID, accepted.FriendID)
	}
}

// 对方主动加我、我通过：接受方（caller）的好友列表必须能看到对方。
// 关系只存单条 record (对方, 我)，caller 落在 friend_id 列上。
func TestFriendUsecase_List_ShowsAcceptedIncomingRequest(t *testing.T) {
	repo := newFakeFriendRepo()
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, &fakeFriendLimiter{}, nil)

	// 2 主动加 1
	res, err := uc.Apply(context.Background(), "uid-2", "uid-1", "")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// 1 通过
	if _, err := uc.Accept(context.Background(), "uid-1", res.RequestID); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	// 1 的好友列表应该有 2
	friends, _, err := uc.List(context.Background(), "uid-1", 0, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(friends) != 1 {
		t.Fatalf("expected 1 friend for the acceptor, got %d", len(friends))
	}
	if friends[0].FriendID != "uid-2" {
		t.Fatalf("FriendID = %s, want uid-2 (the peer, not the record's friend_id)", friends[0].FriendID)
	}
}

// 双向对称：发起方和接受方的列表里都有对方，且 friend_id 都是"对方"。
func TestFriendUsecase_List_BothSidesSeeEachOther(t *testing.T) {
	repo := newFakeFriendRepo()
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, &fakeFriendLimiter{}, nil)

	res, err := uc.Apply(context.Background(), "uid-1", "uid-2", "")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := uc.Accept(context.Background(), "uid-2", res.RequestID); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	for _, tc := range []struct {
		caller string
		peer   string
	}{{"uid-1", "uid-2"}, {"uid-2", "uid-1"}} {
		friends, _, err := uc.List(context.Background(), tc.caller, 0, 10)
		if err != nil {
			t.Fatalf("List(%s): %v", tc.caller, err)
		}
		if len(friends) != 1 {
			t.Fatalf("caller %s: expected 1 friend, got %d", tc.caller, len(friends))
		}
		if friends[0].FriendID != tc.peer {
			t.Fatalf("caller %s: FriendID = %s, want %s", tc.caller, friends[0].FriendID, tc.peer)
		}
	}
}

// 双向语义下 PENDING 仍然不可见：只有 ACCEPTED 才进好友列表。
func TestFriendUsecase_List_ExcludesPendingBothDirections(t *testing.T) {
	repo := newFakeFriendRepo()
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, &fakeFriendLimiter{}, nil)

	// 2 向 1 发起，保持 PENDING
	if _, err := uc.Apply(context.Background(), "uid-2", "uid-1", ""); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	for _, caller := range []string{"uid-1", "uid-2"} {
		friends, _, err := uc.List(context.Background(), caller, 0, 10)
		if err != nil {
			t.Fatalf("List(%s): %v", caller, err)
		}
		if len(friends) != 0 {
			t.Fatalf("caller %s: expected 0 friends (still PENDING), got %d", caller, len(friends))
		}
	}
}

// 双向语义下分页仍然正确：一条关系不重复出现，跨页不丢不重。
func TestFriendUsecase_List_BidirectionalPagination(t *testing.T) {
	repo := newFakeFriendRepo()
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, &fakeFriendLimiter{}, nil)

	// 交替方向建立 4 条关系: 1->2, 3->1, 1->4, 5->1
	for _, tc := range []struct{ from, to string }{
		{"uid-1", "uid-2"}, {"uid-3", "uid-1"}, {"uid-1", "uid-4"}, {"uid-5", "uid-1"},
	} {
		res, err := uc.Apply(context.Background(), tc.from, tc.to, "")
		if err != nil {
			t.Fatalf("Apply(%s->%s): %v", tc.from, tc.to, err)
		}
		if _, err := uc.Accept(context.Background(), tc.to, res.RequestID); err != nil {
			t.Fatalf("Accept(%s->%s): %v", tc.from, tc.to, err)
		}
	}

	// 1 应该看到 2, 3, 4, 5 四个好友
	seen := make(map[string]int)
	var cursor uint64
	for i := 0; i < 10; i++ {
		page, next, err := uc.List(context.Background(), "uid-1", cursor, 2)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, f := range page {
			seen[f.FriendID]++
		}
		if next == 0 {
			break
		}
		cursor = next
	}

	if len(seen) != 4 {
		t.Fatalf("expected 4 distinct friends, got %d: %v", len(seen), seen)
	}
	for peer, cnt := range seen {
		if cnt != 1 {
			t.Fatalf("peer %s appeared %d times, want 1", peer, cnt)
		}
	}
	for _, want := range []string{"uid-2", "uid-3", "uid-4", "uid-5"} {
		if _, ok := seen[want]; !ok {
			t.Fatalf("peer %s missing from friend list", want)
		}
	}
}
