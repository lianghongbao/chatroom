package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	pb "chatroom/friend-service/api/friend/v1"
	"chatroom/friend-service/internal/biz"
	"chatroom/friend-service/internal/testutil"

	kratoserrors "github.com/go-kratos/kratos/v3/errors"
)

// service 层直持 *biz.FriendUsecase，没有窄接口可 fake —— 替身落在
// biz.FriendRepo 这一层，真实 usecase 建在 fake repo 之上。

// fakeFriendRepo 是 biz.FriendRepo 的内存替身。
type fakeFriendRepo struct {
	// forward / reverse 是 FindBothDirections 的返回值。
	forward *biz.Friend
	reverse *biz.Friend

	// saveRet / saveErr 是 Save 的返回值。
	saveRet uint64
	saveErr error

	// updateStatusRet / updateStatusErr 是 UpdateStatus 的返回值。
	updateStatusRet  *biz.Friend
	updateStatusErr  error
	updateStatusCall int

	// updateStatusIfRecipientRet / Err 是 UpdateStatusIfRecipient 的返回值。
	updateStatusIfRecipientRet *biz.Friend
	updateStatusIfRecipientErr error

	// recipientID / recipientStatus / recipientErr 是 GetRecipientAndStatus 的返回值。
	recipientID     string
	recipientStatus biz.FriendStatus
	recipientErr    error

	// deleteByEitherPairDeleted / Err 是 DeleteByEitherPair 的返回值。
	deleteByEitherPairDeleted bool
	deleteByEitherPairErr     error

	// accepted / acceptedErr 是 FindAcceptedByUserIDAndID 的返回值。
	accepted    []*biz.Friend
	acceptedErr error

	// pending / pendingErr 是 FindPendingByRecipientID 的返回值。
	pending    []*biz.Friend
	pendingErr error

	// countPending / countPendingErr 是 CountPendingByRecipientID 的返回值。
	countPending    uint64
	countPendingErr error

	// 记录收到的调用参数。
	saveCalls struct {
		userID, friendID string
		message          string
	}
	listCalls struct {
		userID string
		cursor uint64
		limit  int
	}
	listPendingCalls struct {
		recipientID string
		cursor      uint64
		limit       int
	}
	countPendingCalls     struct{ recipientID string }
	updateStatusIfRecipID struct {
		id          uint64
		recipientID string
	}
}

func (f *fakeFriendRepo) Save(_ context.Context, fr *biz.Friend) (uint64, error) {
	f.saveCalls.userID = fr.UserID
	f.saveCalls.friendID = fr.FriendID
	f.saveCalls.message = fr.Message
	return f.saveRet, f.saveErr
}

func (f *fakeFriendRepo) FindByID(_ context.Context, id uint64) (*biz.Friend, error) {
	if f.forward != nil && f.forward.ID == id {
		return f.forward, nil
	}
	return nil, biz.ErrFriendNotFound
}

func (f *fakeFriendRepo) FindByPair(_ context.Context, userID, friendID string) (*biz.Friend, error) {
	if f.forward != nil && f.forward.UserID == userID && f.forward.FriendID == friendID {
		return f.forward, nil
	}
	return nil, biz.ErrFriendNotFound
}

func (f *fakeFriendRepo) FindBothDirections(_ context.Context, a, b string) (*biz.Friend, *biz.Friend, error) {
	return f.forward, f.reverse, nil
}

func (f *fakeFriendRepo) FindAcceptedByUserIDAndID(_ context.Context, userID string, cursor uint64, limit int) ([]*biz.Friend, error) {
	f.listCalls.userID = userID
	f.listCalls.cursor = cursor
	f.listCalls.limit = limit
	return f.accepted, f.acceptedErr
}

func (f *fakeFriendRepo) FindPendingByRecipientID(_ context.Context, recipientID string, cursor uint64, limit int) ([]*biz.Friend, error) {
	f.listPendingCalls.recipientID = recipientID
	f.listPendingCalls.cursor = cursor
	f.listPendingCalls.limit = limit
	return f.pending, f.pendingErr
}

func (f *fakeFriendRepo) CountPendingByRecipientID(_ context.Context, recipientID string) (uint64, error) {
	f.countPendingCalls.recipientID = recipientID
	return f.countPending, f.countPendingErr
}

func (f *fakeFriendRepo) UpdateStatus(_ context.Context, id uint64, status biz.FriendStatus) (*biz.Friend, error) {
	f.updateStatusCall++
	if f.updateStatusErr != nil {
		return nil, f.updateStatusErr
	}
	if f.updateStatusRet != nil {
		return f.updateStatusRet, nil
	}
	return &biz.Friend{ID: id, Status: status}, nil
}

func (f *fakeFriendRepo) UpdateStatusIfRecipient(_ context.Context, id uint64, recipientID string, from, to biz.FriendStatus) (*biz.Friend, error) {
	f.updateStatusIfRecipID.id = id
	f.updateStatusIfRecipID.recipientID = recipientID
	if f.updateStatusIfRecipientErr != nil {
		return nil, f.updateStatusIfRecipientErr
	}
	if f.updateStatusIfRecipientRet != nil {
		return f.updateStatusIfRecipientRet, nil
	}
	return &biz.Friend{ID: id, FriendID: recipientID, Status: to}, nil
}

func (f *fakeFriendRepo) GetRecipientAndStatus(_ context.Context, id uint64) (string, biz.FriendStatus, error) {
	if f.recipientErr != nil {
		return "", 0, f.recipientErr
	}
	return f.recipientID, f.recipientStatus, nil
}

func (f *fakeFriendRepo) DeleteByPair(_ context.Context, userID, friendID string) error {
	return nil
}

func (f *fakeFriendRepo) DeleteByEitherPair(_ context.Context, callerID, otherID string) (bool, error) {
	return f.deleteByEitherPairDeleted, f.deleteByEitherPairErr
}

// fakeFriendLimiter 是 biz.FriendLimiter 的替身，全部放行。
type fakeFriendLimiter struct{}

func (fakeFriendLimiter) AllowApply(context.Context, string) error        { return nil }
func (fakeFriendLimiter) AllowAcceptReject(context.Context, string) error { return nil }
func (fakeFriendLimiter) AllowRemove(context.Context, string) error       { return nil }

// fakeFriendProfiles 是 biz.FriendProfileFetcher 的替身，返回空列表。
type fakeFriendProfiles struct{}

func (fakeFriendProfiles) FetchProfiles(context.Context, string, []string) ([]*biz.FriendProfile, error) {
	return nil, nil
}

// newService 用 fake repo 造一个真实 usecase + service。
func newService(repo *fakeFriendRepo) *FriendService {
	uc := biz.NewFriendUsecase(repo, fakeFriendProfiles{}, fakeFriendLimiter{}, nil)
	return NewFriendService(uc)
}
func TestServiceApply_NilRequest(t *testing.T) {
	s := newService(&fakeFriendRepo{})
	if _, err := s.ApplyFriend(context.Background(), nil); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceApply_MissingUID(t *testing.T) {
	s := newService(&fakeFriendRepo{})
	if _, err := s.ApplyFriend(context.Background(), &pb.ApplyFriendRequest{TargetId: "uid-2"}); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceApply_ZeroTarget(t *testing.T) {
	s := newService(&fakeFriendRepo{})
	ctx := friendAuthedCtx(t, "uid-1")
	if _, err := s.ApplyFriend(ctx, &pb.ApplyFriendRequest{TargetId: ""}); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceApply_PassesCallerAndTarget(t *testing.T) {
	repo := &fakeFriendRepo{saveRet: 100}
	s := newService(repo)
	ctx := friendAuthedCtx(t, "uid-7")

	reply, err := s.ApplyFriend(ctx, &pb.ApplyFriendRequest{TargetId: "uid-11", Message: "hi"})
	if err != nil {
		t.Fatalf("ApplyFriend() error = %v", err)
	}
	if repo.saveCalls.userID != "uid-7" || repo.saveCalls.friendID != "uid-11" {
		t.Fatalf("calls = %+v", repo.saveCalls)
	}
	if repo.saveCalls.message != "hi" {
		t.Fatalf("message = %q, want hi", repo.saveCalls.message)
	}
	if reply.GetRequestId() != 100 {
		t.Fatalf("reply.RequestId = %d, want 100", reply.GetRequestId())
	}
	if reply.GetStatus() != pb.FriendStatus_FRIEND_STATUS_PENDING {
		t.Fatalf("reply.Status = %v, want PENDING", reply.GetStatus())
	}
}

// TestServiceApply_MessageTruncatedByBiz 验证截断发生在 biz 层而不是
// service 层 —— service 原样透传 message，biz 落库前裁到 200 字符
// （见 biz/friend_test.go TestFriendUsecase_Apply_MessageOver200Truncated）。
func TestServiceApply_MessageTruncatedByBiz(t *testing.T) {
	repo := &fakeFriendRepo{saveRet: 1}
	s := newService(repo)
	ctx := friendAuthedCtx(t, "uid-1")

	longMsg := strings.Repeat("a", 500)
	_, err := s.ApplyFriend(ctx, &pb.ApplyFriendRequest{TargetId: "uid-2", Message: longMsg})
	if err != nil {
		t.Fatalf("ApplyFriend() error = %v", err)
	}
	if got := repo.saveCalls.message; len(got) != 200 {
		t.Fatalf("message length = %d, want 200 (biz 层截断)", len(got))
	}
}

func TestServiceApply_PropagatesError(t *testing.T) {
	// caller == target → biz 层返 ErrFriendSelf。
	s := newService(&fakeFriendRepo{})
	ctx := friendAuthedCtx(t, "uid-1")

	_, err := s.ApplyFriend(ctx, &pb.ApplyFriendRequest{TargetId: "uid-1"})
	if !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}
func TestServiceAccept_NilRequest(t *testing.T) {
	s := newService(&fakeFriendRepo{})
	if _, err := s.AcceptFriend(context.Background(), nil); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceAccept_ZeroRequest(t *testing.T) {
	s := newService(&fakeFriendRepo{})
	ctx := friendAuthedCtx(t, "uid-1")
	if _, err := s.AcceptFriend(ctx, &pb.AcceptFriendRequest{RequestId: 0}); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceAccept_PassesCallerAndRequest(t *testing.T) {
	updated := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	repo := &fakeFriendRepo{
		updateStatusIfRecipientRet: &biz.Friend{
			ID: 99, UserID: "uid-1", FriendID: "uid-7", Status: biz.FriendStatusAccepted, UpdatedAt: updated,
		},
	}
	s := newService(repo)
	ctx := friendAuthedCtx(t, "uid-7")

	reply, err := s.AcceptFriend(ctx, &pb.AcceptFriendRequest{RequestId: 99})
	if err != nil {
		t.Fatalf("AcceptFriend() error = %v", err)
	}
	if repo.updateStatusIfRecipID.id != 99 || repo.updateStatusIfRecipID.recipientID != "uid-7" {
		t.Fatalf("calls = %+v", repo.updateStatusIfRecipID)
	}
	// 归一化：caller 是接收方，「对方」是发起方 user_id=1。
	if reply.GetFriend().GetId() != 99 || reply.GetFriend().GetFriendId() != "uid-1" {
		t.Fatalf("friend = %+v", reply.GetFriend())
	}
	if reply.GetFriend().GetCreatedAtFriend() != updated.Unix() {
		t.Fatalf("CreatedAtFriend = %d, want %d", reply.GetFriend().GetCreatedAtFriend(), updated.Unix())
	}
}
func TestServiceReject_NilRequest(t *testing.T) {
	s := newService(&fakeFriendRepo{})
	if _, err := s.RejectFriend(context.Background(), nil); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceReject_PassesCallerAndRequest(t *testing.T) {
	repo := &fakeFriendRepo{}
	s := newService(repo)
	ctx := friendAuthedCtx(t, "uid-7")

	_, err := s.RejectFriend(ctx, &pb.RejectFriendRequest{RequestId: 99})
	if err != nil {
		t.Fatalf("RejectFriend() error = %v", err)
	}
	if repo.updateStatusIfRecipID.id != 99 || repo.updateStatusIfRecipID.recipientID != "uid-7" {
		t.Fatalf("calls = %+v", repo.updateStatusIfRecipID)
	}
}

func TestServiceReject_PropagatesError(t *testing.T) {
	// 条件 UPDATE 命中 0 行 → 最小探测发现 caller 不是接收方 → FORBIDDEN。
	repo := &fakeFriendRepo{
		updateStatusIfRecipientErr: biz.ErrFriendNotFound,
		recipientID:                "uid-42",
		recipientStatus:            biz.FriendStatusPending,
	}
	s := newService(repo)
	ctx := friendAuthedCtx(t, "uid-7")
	_, err := s.RejectFriend(ctx, &pb.RejectFriendRequest{RequestId: 1})
	if !kratoserrors.IsForbidden(err) {
		t.Fatalf("error = %v, want forbidden", err)
	}
}
func TestServiceRemove_NilRequest(t *testing.T) {
	s := newService(&fakeFriendRepo{})
	if _, err := s.RemoveFriend(context.Background(), nil); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceRemove_ZeroFriend(t *testing.T) {
	s := newService(&fakeFriendRepo{})
	ctx := friendAuthedCtx(t, "uid-1")
	if _, err := s.RemoveFriend(ctx, &pb.RemoveFriendRequest{FriendId: ""}); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceRemove_PassesCallerAndFriend(t *testing.T) {
	repo := &fakeFriendRepo{deleteByEitherPairDeleted: true}
	s := newService(repo)
	ctx := friendAuthedCtx(t, "uid-7")

	_, err := s.RemoveFriend(ctx, &pb.RemoveFriendRequest{FriendId: "uid-11"})
	if err != nil {
		t.Fatalf("RemoveFriend() error = %v", err)
	}
}

func TestServiceRemove_PropagatesError(t *testing.T) {
	// 关系不存在 → biz 返 NOT_FOUND。
	repo := &fakeFriendRepo{deleteByEitherPairDeleted: false}
	s := newService(repo)
	ctx := friendAuthedCtx(t, "uid-7")
	_, err := s.RemoveFriend(ctx, &pb.RemoveFriendRequest{FriendId: "uid-11"})
	if !kratoserrors.IsNotFound(err) {
		t.Fatalf("error = %v, want not found", err)
	}
}
func TestServiceList_NilRequest(t *testing.T) {
	s := newService(&fakeFriendRepo{})
	if _, err := s.ListFriends(context.Background(), nil); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceList_PageSizeClampedTo50(t *testing.T) {
	repo := &fakeFriendRepo{}
	s := newService(repo)
	ctx := friendAuthedCtx(t, "uid-1")

	_, err := s.ListFriends(ctx, &pb.ListFriendsRequest{PageSize: 9999})
	if err != nil {
		t.Fatalf("ListFriends() error = %v", err)
	}
	if repo.listCalls.limit != 50 {
		t.Fatalf("limit = %d, want 50 (clamped)", repo.listCalls.limit)
	}
}

func TestServiceList_PageSizeZeroDefaultsTo50(t *testing.T) {
	repo := &fakeFriendRepo{}
	s := newService(repo)
	ctx := friendAuthedCtx(t, "uid-1")

	_, err := s.ListFriends(ctx, &pb.ListFriendsRequest{PageSize: 0})
	if err != nil {
		t.Fatalf("ListFriends() error = %v", err)
	}
	if repo.listCalls.limit != 50 {
		t.Fatalf("limit = %d, want 50 (default)", repo.listCalls.limit)
	}
}

func TestServiceList_PassesCallerAndCursor(t *testing.T) {
	repo := &fakeFriendRepo{
		accepted: []*biz.Friend{
			{ID: 11, UserID: "uid-7", FriendID: "uid-2"}, {ID: 12, UserID: "uid-7", FriendID: "uid-3"},
		},
	}
	s := newService(repo)
	ctx := friendAuthedCtx(t, "uid-7")

	reply, err := s.ListFriends(ctx, &pb.ListFriendsRequest{Cursor: 5, PageSize: 10})
	if err != nil {
		t.Fatalf("ListFriends() error = %v", err)
	}
	if repo.listCalls.userID != "uid-7" || repo.listCalls.cursor != 5 || repo.listCalls.limit != 10 {
		t.Fatalf("calls = %+v", repo.listCalls)
	}
	if len(reply.GetFriends()) != 2 {
		t.Fatalf("friends count = %d, want 2", len(reply.GetFriends()))
	}
	// 归一化：caller == user_id 时对方是 friend_id。
	if reply.GetFriends()[0].GetFriendId() != "uid-2" || reply.GetFriends()[1].GetFriendId() != "uid-3" {
		t.Fatalf("order = %+v", reply.GetFriends())
	}
}

func TestServiceList_PropagatesError(t *testing.T) {
	wantErr := errors.New("boom")
	repo := &fakeFriendRepo{acceptedErr: wantErr}
	s := newService(repo)
	ctx := friendAuthedCtx(t, "uid-1")
	_, err := s.ListFriends(ctx, &pb.ListFriendsRequest{PageSize: 10})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want boom", err)
	}
}
func TestConvertApplyReply_DefaultPending(t *testing.T) {
	got := convertApplyReply(nil)
	if got == nil {
		t.Fatal("convertApplyReply(nil) = nil, want non-nil")
	}
	if got.GetRequestId() != 0 {
		t.Fatalf("RequestId = %d, want 0", got.GetRequestId())
	}
	if got.GetStatus() != pb.FriendStatus_FRIEND_STATUS_UNSPECIFIED {
		t.Fatalf("Status = %v, want UNSPECIFIED", got.GetStatus())
	}
}

func TestConvertApplyReply_StatusMapping(t *testing.T) {
	cases := []struct {
		in   biz.FriendStatus
		want pb.FriendStatus
	}{
		{biz.FriendStatusPending, pb.FriendStatus_FRIEND_STATUS_PENDING},
		{biz.FriendStatusAccepted, pb.FriendStatus_FRIEND_STATUS_ACCEPTED},
		{biz.FriendStatusRejected, pb.FriendStatus_FRIEND_STATUS_REJECTED},
	}
	for _, c := range cases {
		got := convertApplyReply(&biz.FriendApplyResult{RequestID: 1, Status: c.in})
		if got.GetStatus() != c.want {
			t.Fatalf("status %v -> %v, want %v", c.in, got.GetStatus(), c.want)
		}
	}
}

func TestConvertFriendInfo_Nil(t *testing.T) {
	if got := convertFriendInfo(nil); got != nil {
		t.Fatalf("convertFriendInfo(nil) = %+v, want nil", got)
	}
}

func TestConvertFriendInfo_TimeMapping(t *testing.T) {
	updated := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	f := &biz.Friend{ID: 99, UserID: "uid-1", FriendID: "uid-7", Status: biz.FriendStatusAccepted, UpdatedAt: updated}
	got := convertFriendInfo(f)
	if got.GetId() != 99 || got.GetFriendId() != "uid-7" {
		t.Fatalf("id/friend_id mapping wrong: %+v", got)
	}
	if got.GetCreatedAtFriend() != updated.Unix() {
		t.Fatalf("CreatedAtFriend = %d, want %d", got.GetCreatedAtFriend(), updated.Unix())
	}
	// convertFriendInfo 仅填关系记录字段；user profile 字段由
	// FriendProfileFetcher 在 biz 层补全。
	if got.GetNickname() != "" || got.GetAvatar() != "" {
		t.Fatalf("user profile fields should be empty, got %+v", got)
	}
}
func TestServiceListPendingRequests_PassesArgs(t *testing.T) {
	repo := &fakeFriendRepo{
		pending: []*biz.Friend{{ID: 1, UserID: "uid-2", FriendID: "uid-1", Status: biz.FriendStatusPending}},
	}
	s := newService(repo)
	out, err := s.ListPendingRequests(friendAuthedCtx(t, "uid-1"), &pb.ListPendingRequestsRequest{Cursor: 5, PageSize: 10})
	if err != nil {
		t.Fatalf("ListPendingRequests: %v", err)
	}
	if repo.listPendingCalls.recipientID != "uid-1" {
		t.Fatalf("recipientID = %s, want uid-1", repo.listPendingCalls.recipientID)
	}
	// biz 层多取一条用来判断「还有没有下一页」，所以 repo 收到 limit+1。
	if repo.listPendingCalls.cursor != 5 || repo.listPendingCalls.limit != 11 {
		t.Fatalf("cursor/limit = %d/%d, want 5/11", repo.listPendingCalls.cursor, repo.listPendingCalls.limit)
	}
	if len(out.GetRequests()) != 1 {
		t.Fatalf("requests len = %d, want 1", len(out.GetRequests()))
	}
}

func TestServiceListPendingRequests_DefaultPageSize(t *testing.T) {
	repo := &fakeFriendRepo{}
	s := newService(repo)
	// pageSize 留空 (0) → service 层折叠到 50
	if _, err := s.ListPendingRequests(friendAuthedCtx(t, "uid-1"), &pb.ListPendingRequestsRequest{}); err != nil {
		t.Fatalf("ListPendingRequests: %v", err)
	}
	if repo.listPendingCalls.limit != 51 {
		t.Fatalf("default limit = %d, want 51 (biz 多取一条判断下一页)", repo.listPendingCalls.limit)
	}
}

func TestServiceCountPending_PassesCallerID(t *testing.T) {
	repo := &fakeFriendRepo{countPending: 7}
	s := newService(repo)
	out, err := s.CountPending(friendAuthedCtx(t, "uid-42"), &pb.CountPendingRequest{})
	if err != nil {
		t.Fatalf("CountPending: %v", err)
	}
	if repo.countPendingCalls.recipientID != "uid-42" {
		t.Fatalf("recipientID = %s, want uid-42", repo.countPendingCalls.recipientID)
	}
	if out.GetCount() != 7 {
		t.Fatalf("count = %d, want 7", out.GetCount())
	}
}

// friendAuthedCtx 用一次 auth.Server 中间件把 uid 注入 ctx，复用 user_test.go
// 里的 jwtFor（同一 secret），便于跨测试共用 token 格式。
func friendAuthedCtx(t *testing.T, uid string) context.Context {
	t.Helper()
	return testutil.AuthedCtxWithToken(t, testutil.JWTFor(t, "test-secret", uid), "test-secret")
}
