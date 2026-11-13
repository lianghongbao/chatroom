package service

import (
	"context"
	"testing"
	"time"

	pb "chatroom/api/conversation/service/v1"
	"chatroom/app/conversation/service/internal/biz"
	"chatroom/pkg/testutil"

	kratoserrors "github.com/go-kratos/kratos/v3/errors"
)

// service 层直持 *biz.ConversationUsecase，没有窄接口可 fake —— 替身落在
// biz.ConversationRepo 这一层，真实 usecase 建在 fake repo 之上。

// fakeConvRepo 是 biz.ConversationRepo 的内存替身。
type fakeConvRepo struct {
	// conv 是 FindByID / Save 返回的会话。
	conv *biz.Conversation
	// convErr 是 FindByID 的错误。
	convErr error

	// single 是 FindSingleByMembers 的返回值；nil 表示「还没建过」。
	single *biz.Conversation

	// savedReused 是 Save 返回的复用标志。
	savedReused bool

	// listRet / listErr 是 ListByUserID 的返回值。
	listRet []*biz.Conversation
	listErr error

	// isMember 是 IsMember 的返回值。
	isMember bool

	// addMembersAdded / addMembersTotal 是 AddMembers 的返回值。
	addMembersAdded int
	addMembersTotal int

	// removeMemberTotal 是 RemoveMember 的返回值。
	removeMemberTotal int

	// updated 是 UpdateName / TransferOwner 的返回值。
	updated *biz.Conversation

	// dissolveErr 是 Dissolve 的错误。
	dissolveErr error

	// unread 是 UpdateUnread / GetUnread / ClearUnread 的返回值。
	unread uint32

	// listUnread 是 ListUnread 的返回值。
	listUnread map[uint64]uint32

	// 记录收到的调用参数。
	listCalls struct {
		userID string
		cursor biz.ConversationCursor
		limit  int
	}
	addMembersCalls struct {
		conversationID uint64
		memberIDs      []string
	}
	removeMemberCalls struct {
		conversationID uint64
		userID         string
	}
	unreadCalls struct {
		conversationID uint64
		userID         string
		delta          int32
	}
	unreadBatchCalls struct {
		conversationID uint64
		userIDs        []string
		delta          int32
	}
}

func (f *fakeConvRepo) Save(_ context.Context, c *biz.Conversation) (*biz.Conversation, bool, error) {
	saved := f.conv
	if saved == nil {
		saved = c
		saved.ID = 1
	}
	return saved, f.savedReused, nil
}

func (f *fakeConvRepo) FindByID(_ context.Context, id uint64) (*biz.Conversation, error) {
	if f.convErr != nil {
		return nil, f.convErr
	}
	if f.conv == nil {
		return nil, biz.ErrConversationNotFound
	}
	return f.conv, nil
}

func (f *fakeConvRepo) FindSingleByMembers(_ context.Context, a, b string) (*biz.Conversation, error) {
	if f.single == nil {
		return nil, biz.ErrConversationNotFound
	}
	return f.single, nil
}

func (f *fakeConvRepo) ListByUserID(_ context.Context, userID string, cursor biz.ConversationCursor, limit int) ([]*biz.Conversation, error) {
	f.listCalls.userID = userID
	f.listCalls.cursor = cursor
	f.listCalls.limit = limit
	return f.listRet, f.listErr
}

func (f *fakeConvRepo) IsMember(_ context.Context, conversationID uint64, userID string) (bool, error) {
	return f.isMember, nil
}

func (f *fakeConvRepo) AddMembers(_ context.Context, conversationID uint64, userIDs []string) (int, int, error) {
	f.addMembersCalls.conversationID = conversationID
	f.addMembersCalls.memberIDs = userIDs
	return f.addMembersAdded, f.addMembersTotal, nil
}

func (f *fakeConvRepo) RemoveMember(_ context.Context, conversationID uint64, userID string) (int, error) {
	f.removeMemberCalls.conversationID = conversationID
	f.removeMemberCalls.userID = userID
	return f.removeMemberTotal, nil
}

func (f *fakeConvRepo) UpdateName(_ context.Context, id uint64, name string) (*biz.Conversation, error) {
	if f.updated != nil {
		return f.updated, nil
	}
	return f.conv, nil
}

func (f *fakeConvRepo) TransferOwner(_ context.Context, conversationID uint64, newOwnerID string) (*biz.Conversation, error) {
	if f.updated != nil {
		return f.updated, nil
	}
	return f.conv, nil
}

func (f *fakeConvRepo) Dissolve(_ context.Context, conversationID uint64) error {
	return f.dissolveErr
}

func (f *fakeConvRepo) TouchConversation(_ context.Context, id uint64) error {
	return nil
}

func (f *fakeConvRepo) UpdateUnread(_ context.Context, conversationID uint64, userID string, delta int32) (uint32, error) {
	f.unreadCalls.conversationID = conversationID
	f.unreadCalls.userID = userID
	f.unreadCalls.delta = delta
	return f.unread, nil
}

func (f *fakeConvRepo) UpdateUnreadBatch(_ context.Context, conversationID uint64, userIDs []string, delta int32) (uint32, error) {
	f.unreadBatchCalls.conversationID = conversationID
	f.unreadBatchCalls.userIDs = userIDs
	f.unreadBatchCalls.delta = delta
	return uint32(len(userIDs)), nil
}

func (f *fakeConvRepo) GetUnread(_ context.Context, conversationID uint64, userID string) (uint32, error) {
	f.unreadCalls.conversationID = conversationID
	f.unreadCalls.userID = userID
	return f.unread, nil
}

func (f *fakeConvRepo) ClearUnread(_ context.Context, conversationID uint64, userID string) (uint32, error) {
	f.unreadCalls.conversationID = conversationID
	f.unreadCalls.userID = userID
	return f.unread, nil
}

func (f *fakeConvRepo) ListUnread(_ context.Context, userID string) (map[uint64]uint32, error) {
	return f.listUnread, nil
}

// fakeConvProfiles 是 biz.ConversationProfileFetcher 的替身。
// 拉不到资料时 biz 会把昵称 / 头像填成等长的空串占位 —— 所以要验证昵称
// 真的透传到 DTO，替身必须返回资料。
type fakeConvProfiles struct {
	profiles map[string]*biz.UserProfile
}

func (f fakeConvProfiles) FetchProfiles(_ context.Context, ids []string) ([]*biz.UserProfile, error) {
	out := make([]*biz.UserProfile, 0, len(ids))
	for _, id := range ids {
		if p, ok := f.profiles[id]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

// fakeConvExistence 是 biz.UserExistenceChecker 的替身：所有人都存在。
type fakeConvExistence struct{}

func (fakeConvExistence) CheckUsersExist(context.Context, []string) ([]string, error) {
	return nil, nil
}

// newService 用 fake repo 造一个真实 usecase + service。
func newService(repo *fakeConvRepo) *ConversationService {
	return newServiceWithProfiles(repo, fakeConvProfiles{})
}

// newServiceWithProfiles 同 newService，但自带一份资料替身。
func newServiceWithProfiles(repo *fakeConvRepo, profiles biz.ConversationProfileFetcher) *ConversationService {
	uc := biz.NewConversationUsecase(repo, profiles, fakeConvExistence{}, nil, nil)
	return NewConversationService(uc)
}

const convTestSecret = "test-secret-32-bytes-long-xxxxxx"

// convAuthedCtx 用一次 auth.Server 中间件把 uid 注入 ctx。
func convAuthedCtx(t *testing.T, uid string) context.Context {
	t.Helper()
	return testutil.AuthedCtxWithToken(t, testutil.JWTFor(t, convTestSecret, uid), convTestSecret)
}

// groupConv 一个 2 人群聊，owner 是 uid 1。
func groupConv() *biz.Conversation {
	return &biz.Conversation{
		ID: 7, Type: biz.ConversationTypeGroup, Name: "群", OwnerID: "uid-1",
		MemberIDs: []string{"uid-1", "uid-2"}, MemberCount: 2,
	}
}
func TestServiceConversation_Create_NilRequest(t *testing.T) {
	s := newService(&fakeConvRepo{})
	if _, err := s.CreateConversation(context.Background(), nil); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceConversation_Create_NoAuthUID(t *testing.T) {
	s := newService(&fakeConvRepo{})
	_, err := s.CreateConversation(context.Background(), &pb.CreateConversationRequest{MemberIds: []string{"uid-1", "uid-2"}})
	if !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceConversation_Create_EmptyMembers(t *testing.T) {
	s := newService(&fakeConvRepo{})
	ctx := convAuthedCtx(t, "uid-1")
	_, err := s.CreateConversation(ctx, &pb.CreateConversationRequest{})
	if !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceConversation_Create_PassesFieldsToUsecase(t *testing.T) {
	repo := &fakeConvRepo{conv: &biz.Conversation{
		ID: 7, Type: biz.ConversationTypeGroup, Name: "群",
		OwnerID: "uid-1", MemberIDs: []string{"uid-1", "uid-2"}, MemberCount: 2,
	}}
	s := newService(repo)
	ctx := convAuthedCtx(t, "uid-1")

	reply, err := s.CreateConversation(ctx, &pb.CreateConversationRequest{
		Type:      pb.ConversationType_CONVERSATION_TYPE_GROUP,
		MemberIds: []string{"uid-1", "uid-2"},
		Name:      "群",
	})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if reply.GetConversation().GetId() != 7 {
		t.Fatalf("reply id = %d, want 7", reply.GetConversation().GetId())
	}
	if reply.GetConversation().GetType() != pb.ConversationType_CONVERSATION_TYPE_GROUP {
		t.Fatalf("reply type = %v, want group", reply.GetConversation().GetType())
	}
	if reply.GetConversation().GetName() != "群" {
		t.Fatalf("reply name = %q, want 群", reply.GetConversation().GetName())
	}
}

func TestServiceConversation_Create_PropagatesReused(t *testing.T) {
	repo := &fakeConvRepo{
		single:      &biz.Conversation{ID: 9, Type: biz.ConversationTypeSingle},
		savedReused: true,
	}
	s := newService(repo)
	ctx := convAuthedCtx(t, "uid-1")

	reply, err := s.CreateConversation(ctx, &pb.CreateConversationRequest{MemberIds: []string{"uid-1", "uid-2"}})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if !reply.GetReused() {
		t.Fatal("reused 应为 true")
	}
}
func TestServiceConversation_List_NilRequest(t *testing.T) {
	s := newService(&fakeConvRepo{})
	if _, err := s.ListConversations(context.Background(), nil); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceConversation_List_PassesCursorAndPageSize(t *testing.T) {
	repo := &fakeConvRepo{listRet: []*biz.Conversation{
		{ID: 3, Type: biz.ConversationTypeGroup},
		{ID: 4, Type: biz.ConversationTypeGroup},
	}}
	s := newService(repo)
	ctx := convAuthedCtx(t, "uid-1")

	reply, err := s.ListConversations(ctx, &pb.ListConversationsRequest{Cursor: 5, PageSize: 10})
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if repo.listCalls.userID != "uid-1" {
		t.Fatalf("callerID = %q, want uid-1", repo.listCalls.userID)
	}
	if repo.listCalls.cursor.ID != 5 {
		t.Fatalf("cursor = %d, want 5", repo.listCalls.cursor.ID)
	}
	// biz 层多取一条判断「还有没有下一页」，所以 repo 收到 limit+1。
	if repo.listCalls.limit != 11 {
		t.Fatalf("limit = %d, want 11", repo.listCalls.limit)
	}
	if len(reply.GetConversations()) != 2 {
		t.Fatalf("conversations = %d, want 2", len(reply.GetConversations()))
	}
	// 没拿到第 limit+1 条 → 尾页，next_cursor = 0。
	if reply.GetNextCursor() != 0 {
		t.Fatalf("next_cursor = %d, want 0", reply.GetNextCursor())
	}
}

func TestServiceConversation_List_HasMoreSetsNextCursor(t *testing.T) {
	// repo 返回 limit+1 条 → 还有下一页，next_cursor = 本页最后一条的 id。
	repo := &fakeConvRepo{}
	for i := 0; i < 11; i++ {
		repo.listRet = append(repo.listRet, &biz.Conversation{ID: uint64(i + 1)})
	}
	s := newService(repo)
	ctx := convAuthedCtx(t, "uid-1")

	reply, err := s.ListConversations(ctx, &pb.ListConversationsRequest{PageSize: 10})
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(reply.GetConversations()) != 10 {
		t.Fatalf("conversations = %d, want 10", len(reply.GetConversations()))
	}
	if reply.GetNextCursor() != 10 {
		t.Fatalf("next_cursor = %d, want 10", reply.GetNextCursor())
	}
}

func TestServiceConversation_List_ClampsPageSize(t *testing.T) {
	repo := &fakeConvRepo{}
	s := newService(repo)
	ctx := convAuthedCtx(t, "uid-1")

	// page_size=0 → 默认 50。
	if _, err := s.ListConversations(ctx, &pb.ListConversationsRequest{PageSize: 0}); err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if repo.listCalls.limit != 51 {
		t.Fatalf("limit = %d, want 51 (50 + biz 多取一条)", repo.listCalls.limit)
	}

	// page_size=9999 → 折叠到 50。
	if _, err := s.ListConversations(ctx, &pb.ListConversationsRequest{PageSize: 9999}); err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if repo.listCalls.limit != 51 {
		t.Fatalf("limit = %d, want 51 (50 + biz 多取一条)", repo.listCalls.limit)
	}
}
func TestServiceConversation_Get_ZeroID(t *testing.T) {
	s := newService(&fakeConvRepo{})
	ctx := convAuthedCtx(t, "uid-1")
	if _, err := s.GetConversation(ctx, &pb.GetConversationRequest{Id: 0}); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceConversation_Get_PassesIDToUsecase(t *testing.T) {
	repo := &fakeConvRepo{conv: &biz.Conversation{ID: 3, Type: biz.ConversationTypeGroup}, isMember: true}
	s := newService(repo)
	ctx := convAuthedCtx(t, "uid-1")

	reply, err := s.GetConversation(ctx, &pb.GetConversationRequest{Id: 3})
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}
	if reply.GetConversation().GetId() != 3 {
		t.Fatalf("id = %d, want 3", reply.GetConversation().GetId())
	}
}

func TestServiceConversation_Get_NotMemberFoldsToNotFound(t *testing.T) {
	// 非成员看到的是 NOT_FOUND —— 不泄露「会话存在但你不是成员」。
	repo := &fakeConvRepo{conv: &biz.Conversation{ID: 3}, isMember: false}
	s := newService(repo)
	ctx := convAuthedCtx(t, "uid-1")

	_, err := s.GetConversation(ctx, &pb.GetConversationRequest{Id: 3})
	if !kratoserrors.IsNotFound(err) {
		t.Fatalf("error = %v, want not found", err)
	}
}
func TestServiceConversation_AddMembers_ZeroConversationID(t *testing.T) {
	s := newService(&fakeConvRepo{})
	ctx := convAuthedCtx(t, "uid-1")
	_, err := s.AddMembers(ctx, &pb.AddMembersRequest{MemberIds: []string{"uid-2"}})
	if !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceConversation_AddMembers_EmptyMembers(t *testing.T) {
	s := newService(&fakeConvRepo{})
	ctx := convAuthedCtx(t, "uid-1")
	_, err := s.AddMembers(ctx, &pb.AddMembersRequest{ConversationId: 1})
	if !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceConversation_AddMembers_ReturnsCounts(t *testing.T) {
	repo := &fakeConvRepo{
		conv:            groupConv(),
		addMembersAdded: 2,
		addMembersTotal: 5,
	}
	s := newService(repo)
	ctx := convAuthedCtx(t, "uid-1")

	reply, err := s.AddMembers(ctx, &pb.AddMembersRequest{ConversationId: 7, MemberIds: []string{"uid-3", "uid-4"}})
	if err != nil {
		t.Fatalf("AddMembers: %v", err)
	}
	if reply.GetMemberCount() != 5 {
		t.Fatalf("member_count = %d, want 5", reply.GetMemberCount())
	}
	if reply.GetAddedCount() != 2 {
		t.Fatalf("added_count = %d, want 2", reply.GetAddedCount())
	}
	if repo.addMembersCalls.conversationID != 7 {
		t.Fatalf("conversation_id = %d, want 7", repo.addMembersCalls.conversationID)
	}
	if len(repo.addMembersCalls.memberIDs) != 2 {
		t.Fatalf("member_ids = %v, want 2 个", repo.addMembersCalls.memberIDs)
	}
}

func TestServiceConversation_AddMembers_NotOwnerForbidden(t *testing.T) {
	// owner 是 1，caller 是 2 → FORBIDDEN。
	repo := &fakeConvRepo{conv: groupConv()}
	s := newService(repo)
	ctx := convAuthedCtx(t, "uid-2")

	_, err := s.AddMembers(ctx, &pb.AddMembersRequest{ConversationId: 7, MemberIds: []string{"uid-3"}})
	if !kratoserrors.IsForbidden(err) {
		t.Fatalf("error = %v, want forbidden", err)
	}
}
func TestServiceConversation_RemoveMember_ZeroMemberID(t *testing.T) {
	s := newService(&fakeConvRepo{})
	ctx := convAuthedCtx(t, "uid-1")
	_, err := s.RemoveMember(ctx, &pb.RemoveMemberRequest{ConversationId: 1})
	if !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceConversation_RemoveMember_ReturnsCount(t *testing.T) {
	repo := &fakeConvRepo{conv: groupConv(), removeMemberTotal: 3}
	s := newService(repo)
	ctx := convAuthedCtx(t, "uid-1")

	reply, err := s.RemoveMember(ctx, &pb.RemoveMemberRequest{ConversationId: 7, MemberId: "uid-2"})
	if err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if reply.GetMemberCount() != 3 {
		t.Fatalf("member_count = %d, want 3", reply.GetMemberCount())
	}
	if repo.removeMemberCalls.userID != "uid-2" {
		t.Fatalf("member_id = %q, want uid-2", repo.removeMemberCalls.userID)
	}
}
func TestServiceConversation_Leave_ZeroConversationID(t *testing.T) {
	s := newService(&fakeConvRepo{})
	ctx := convAuthedCtx(t, "uid-1")
	if _, err := s.LeaveConversation(ctx, &pb.LeaveConversationRequest{}); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceConversation_Leave_PassesIDToUsecase(t *testing.T) {
	repo := &fakeConvRepo{conv: groupConv()}
	s := newService(repo)
	ctx := convAuthedCtx(t, "uid-2")

	if _, err := s.LeaveConversation(ctx, &pb.LeaveConversationRequest{ConversationId: 7}); err != nil {
		t.Fatalf("LeaveConversation: %v", err)
	}
	if repo.removeMemberCalls.conversationID != 7 || repo.removeMemberCalls.userID != "uid-2" {
		t.Fatalf("calls = %+v", repo.removeMemberCalls)
	}
}

func TestServiceConversation_Leave_OwnerCannotLeave(t *testing.T) {
	// 群主退群要走 TransferOwner / Dissolve。
	repo := &fakeConvRepo{conv: groupConv()}
	s := newService(repo)
	ctx := convAuthedCtx(t, "uid-1")

	_, err := s.LeaveConversation(ctx, &pb.LeaveConversationRequest{ConversationId: 7})
	if !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}
func TestServiceConversation_Update_ZeroConversationID(t *testing.T) {
	s := newService(&fakeConvRepo{})
	ctx := convAuthedCtx(t, "uid-1")
	if _, err := s.UpdateConversation(ctx, &pb.UpdateConversationRequest{Name: "新名"}); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceConversation_Update_ReturnsConversation(t *testing.T) {
	repo := &fakeConvRepo{
		conv:    groupConv(),
		updated: &biz.Conversation{ID: 4, Name: "新名"},
	}
	s := newService(repo)
	ctx := convAuthedCtx(t, "uid-1")

	reply, err := s.UpdateConversation(ctx, &pb.UpdateConversationRequest{ConversationId: 4, Name: "新名"})
	if err != nil {
		t.Fatalf("UpdateConversation: %v", err)
	}
	if reply.GetConversation().GetName() != "新名" {
		t.Fatalf("name = %q, want 新名", reply.GetConversation().GetName())
	}
}
func TestConvertConversation_MapsAllFields(t *testing.T) {
	created := time.Unix(1700000000, 0).UTC()
	updated := time.Unix(1700000100, 0).UTC()
	c := &biz.Conversation{
		ID: 11, Type: biz.ConversationTypeGroup, Name: "群", OwnerID: "uid-3",
		MemberIDs: []string{"uid-1", "uid-2", "uid-3"}, MemberCount: 3,
		MemberNicknames: []string{"A", "B", "C"},
		MemberAvatars:   []string{"a", "b", "c"},
		LastMessage:     "hi", LastMessageAt: updated,
		CreatedAt: created, UpdatedAt: updated,
	}
	got := convertConversation(c)
	if got.GetId() != 11 {
		t.Fatalf("id = %d, want 11", got.GetId())
	}
	if got.GetType() != pb.ConversationType_CONVERSATION_TYPE_GROUP {
		t.Fatalf("type = %v, want group", got.GetType())
	}
	if got.GetName() != "群" {
		t.Fatalf("name = %q", got.GetName())
	}
	if got.GetOwnerId() != "uid-3" {
		t.Fatalf("owner_id = %q, want uid-3", got.GetOwnerId())
	}
	if got.GetMemberCount() != 3 {
		t.Fatalf("member_count = %d, want 3", got.GetMemberCount())
	}
	if len(got.GetMemberIds()) != 3 {
		t.Fatalf("member_ids = %v", got.GetMemberIds())
	}
	if len(got.GetMemberNicknames()) != 3 || got.GetMemberNicknames()[0] != "A" {
		t.Fatalf("member_nicknames = %v", got.GetMemberNicknames())
	}
	if len(got.GetMemberAvatars()) != 3 || got.GetMemberAvatars()[0] != "a" {
		t.Fatalf("member_avatars = %v", got.GetMemberAvatars())
	}
	if got.GetLastMessage() != "hi" {
		t.Fatalf("last_message = %q", got.GetLastMessage())
	}
	if got.GetLastMessageAt() != updated.Unix() {
		t.Fatalf("last_message_at = %d, want %d", got.GetLastMessageAt(), updated.Unix())
	}
	if got.GetCreatedAt() != created.Unix() {
		t.Fatalf("created_at = %d, want %d", got.GetCreatedAt(), created.Unix())
	}
	if got.GetUpdatedAt() != updated.Unix() {
		t.Fatalf("updated_at = %d, want %d", got.GetUpdatedAt(), updated.Unix())
	}
}

func TestConvertConversation_NilReturnsNil(t *testing.T) {
	if got := convertConversation(nil); got != nil {
		t.Fatalf("nil 会话应返回 nil，实际 %v", got)
	}
}

func TestConvertConversation_EmptyLastMessageStaysZero(t *testing.T) {
	// 消息服务上线前 last_message 应为空、last_message_at 为 0。
	c := &biz.Conversation{ID: 1, Type: biz.ConversationTypeSingle}
	got := convertConversation(c)
	if got.GetLastMessage() != "" {
		t.Fatalf("last_message = %q, want 空", got.GetLastMessage())
	}
	if got.GetLastMessageAt() != 0 {
		t.Fatalf("last_message_at = %d, want 0", got.GetLastMessageAt())
	}
}

func TestConvertConversation_SingleTypeMapsToSingleEnum(t *testing.T) {
	got := convertConversation(&biz.Conversation{ID: 1, Type: biz.ConversationTypeSingle})
	if got.GetType() != pb.ConversationType_CONVERSATION_TYPE_SINGLE {
		t.Fatalf("type = %v, want single", got.GetType())
	}
}
func TestServiceConversation_TransferOwner_NilRequest(t *testing.T) {
	s := newService(&fakeConvRepo{})
	if _, err := s.TransferOwner(context.Background(), nil); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceConversation_TransferOwner_ZeroConversationID(t *testing.T) {
	s := newService(&fakeConvRepo{})
	ctx := convAuthedCtx(t, "uid-1")
	if _, err := s.TransferOwner(ctx, &pb.TransferOwnerRequest{NewOwnerId: "uid-2"}); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceConversation_TransferOwner_ZeroNewOwner(t *testing.T) {
	s := newService(&fakeConvRepo{})
	ctx := convAuthedCtx(t, "uid-1")
	if _, err := s.TransferOwner(ctx, &pb.TransferOwnerRequest{ConversationId: 1}); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceConversation_TransferOwner_NoAuth(t *testing.T) {
	s := newService(&fakeConvRepo{})
	if _, err := s.TransferOwner(context.Background(), &pb.TransferOwnerRequest{ConversationId: 1, NewOwnerId: "uid-2"}); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceConversation_TransferOwner_PassesThrough(t *testing.T) {
	repo := &fakeConvRepo{
		conv:    groupConv(),
		updated: &biz.Conversation{ID: 7, OwnerID: "uid-2", Type: biz.ConversationTypeGroup},
	}
	s := newService(repo)
	ctx := convAuthedCtx(t, "uid-1")

	reply, err := s.TransferOwner(ctx, &pb.TransferOwnerRequest{ConversationId: 7, NewOwnerId: "uid-2"})
	if err != nil {
		t.Fatalf("TransferOwner: %v", err)
	}
	if reply.GetConversation().GetOwnerId() != "uid-2" {
		t.Fatalf("owner_id = %q, want uid-2", reply.GetConversation().GetOwnerId())
	}
}
func TestServiceConversation_Dissolve_NilRequest(t *testing.T) {
	s := newService(&fakeConvRepo{})
	if _, err := s.DissolveConversation(context.Background(), nil); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceConversation_Dissolve_ZeroConversationID(t *testing.T) {
	s := newService(&fakeConvRepo{})
	ctx := convAuthedCtx(t, "uid-1")
	if _, err := s.DissolveConversation(ctx, &pb.DissolveConversationRequest{}); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceConversation_Dissolve_NoAuth(t *testing.T) {
	s := newService(&fakeConvRepo{})
	if _, err := s.DissolveConversation(context.Background(), &pb.DissolveConversationRequest{ConversationId: 1}); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceConversation_Dissolve_PassesThrough(t *testing.T) {
	repo := &fakeConvRepo{conv: groupConv()}
	s := newService(repo)
	ctx := convAuthedCtx(t, "uid-1")

	if _, err := s.DissolveConversation(ctx, &pb.DissolveConversationRequest{ConversationId: 7}); err != nil {
		t.Fatalf("DissolveConversation: %v", err)
	}
}
func TestServiceConversation_ListMembers_NilRequest(t *testing.T) {
	s := newService(&fakeConvRepo{})
	if _, err := s.ListMembers(context.Background(), nil); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceConversation_ListMembers_ZeroConversationID(t *testing.T) {
	s := newService(&fakeConvRepo{})
	ctx := convAuthedCtx(t, "uid-1")
	if _, err := s.ListMembers(ctx, &pb.ListMembersRequest{}); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceConversation_ListMembers_NoAuth(t *testing.T) {
	s := newService(&fakeConvRepo{})
	if _, err := s.ListMembers(context.Background(), &pb.ListMembersRequest{ConversationId: 1}); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceConversation_ListMembers_ReturnsMembers(t *testing.T) {
	repo := &fakeConvRepo{
		conv: &biz.Conversation{
			ID:          3,
			MemberIDs:   []string{"uid-1", "uid-2"},
			MemberCount: 2,
		},
		isMember: true,
	}
	profiles := fakeConvProfiles{profiles: map[string]*biz.UserProfile{
		"uid-1": {UserID: "uid-1", Nickname: "Alice", Avatar: "a.png"},
		"uid-2": {UserID: "uid-2", Nickname: "Bob", Avatar: "b.png"},
	}}
	s := newServiceWithProfiles(repo, profiles)
	ctx := convAuthedCtx(t, "uid-1")

	reply, err := s.ListMembers(ctx, &pb.ListMembersRequest{ConversationId: 3})
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if len(reply.GetMemberIds()) != 2 {
		t.Fatalf("member_ids = %v, want 2 个", reply.GetMemberIds())
	}
	if len(reply.GetMemberNicknames()) != 2 || reply.GetMemberNicknames()[0] != "Alice" {
		t.Fatalf("member_nicknames = %v", reply.GetMemberNicknames())
	}
	if len(reply.GetMemberAvatars()) != 2 || reply.GetMemberAvatars()[1] != "b.png" {
		t.Fatalf("member_avatars = %v", reply.GetMemberAvatars())
	}
}
func TestServiceConversation_Create_InvalidType(t *testing.T) {
	s := newService(&fakeConvRepo{})
	ctx := convAuthedCtx(t, "uid-1")

	// type=99 不在枚举范围内 —— 不该被当成合法值传进 biz 层。
	if _, err := s.CreateConversation(ctx, &pb.CreateConversationRequest{
		Type:      pb.ConversationType(99),
		MemberIds: []string{"uid-1", "uid-2"},
	}); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceConversation_Create_NameTooLong(t *testing.T) {
	s := newService(&fakeConvRepo{})
	ctx := convAuthedCtx(t, "uid-1")

	long := make([]byte, biz.MaxConversationNameLen+1)
	for i := range long {
		long[i] = 'x'
	}
	if _, err := s.CreateConversation(ctx, &pb.CreateConversationRequest{
		MemberIds: []string{"uid-1", "uid-2"},
		Name:      string(long),
	}); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceConversation_Create_UnspecifiedTypeInfers(t *testing.T) {
	// UNSPECIFIED → biz 按成员数推断单聊。
	repo := &fakeConvRepo{conv: &biz.Conversation{ID: 1, Type: biz.ConversationTypeSingle}}
	s := newService(repo)
	ctx := convAuthedCtx(t, "uid-1")

	if _, err := s.CreateConversation(ctx, &pb.CreateConversationRequest{
		MemberIds: []string{"uid-1", "uid-2"},
	}); err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
}
func TestServiceConversation_ClearUnread_NilRequest(t *testing.T) {
	s := newService(&fakeConvRepo{})
	if _, err := s.ClearUnread(context.Background(), nil); err == nil {
		t.Fatal("nil 请求应该返错")
	}
}

func TestServiceConversation_ClearUnread_ZeroArgs(t *testing.T) {
	s := newService(&fakeConvRepo{})
	if _, err := s.ClearUnread(context.Background(), &pb.ClearUnreadRequest{UserId: "uid-1"}); err == nil {
		t.Fatal("conversation_id=0 应该返错")
	}
	if _, err := s.ClearUnread(context.Background(), &pb.ClearUnreadRequest{ConversationId: 1}); err == nil {
		t.Fatal("user_id=0 应该返错")
	}
}

func TestServiceConversation_ClearUnread_PassesThrough(t *testing.T) {
	repo := &fakeConvRepo{unread: 0}
	s := newService(repo)

	if _, err := s.ClearUnread(context.Background(), &pb.ClearUnreadRequest{
		ConversationId: 7,
		UserId:         "uid-42",
	}); err != nil {
		t.Fatalf("ClearUnread: %v", err)
	}
	if repo.unreadCalls.conversationID != 7 || repo.unreadCalls.userID != "uid-42" {
		t.Errorf("参数传错了: conv=%d user=%q", repo.unreadCalls.conversationID, repo.unreadCalls.userID)
	}
}

func TestServiceConversation_UpdateUnreadBatch_NilRequest(t *testing.T) {
	s := newService(&fakeConvRepo{})
	if _, err := s.UpdateUnreadBatch(context.Background(), nil); err == nil {
		t.Fatal("nil 请求应该返错")
	}
}

func TestServiceConversation_UpdateUnreadBatch_ZeroArgs(t *testing.T) {
	s := newService(&fakeConvRepo{})
	if _, err := s.UpdateUnreadBatch(context.Background(), &pb.UpdateUnreadBatchRequest{UserIds: []string{"uid-1"}}); err == nil {
		t.Fatal("conversation_id=0 应该返错")
	}
	if _, err := s.UpdateUnreadBatch(context.Background(), &pb.UpdateUnreadBatchRequest{ConversationId: 1}); err == nil {
		t.Fatal("user_ids 为空应该返错")
	}
}

func TestServiceConversation_UpdateUnreadBatch_PassesThrough(t *testing.T) {
	repo := &fakeConvRepo{}
	s := newService(repo)

	reply, err := s.UpdateUnreadBatch(context.Background(), &pb.UpdateUnreadBatchRequest{
		ConversationId: 7,
		UserIds:        []string{"uid-1", "uid-2"},
		Delta:          1,
	})
	if err != nil {
		t.Fatalf("UpdateUnreadBatch: %v", err)
	}
	if reply.GetUpdated() != 2 {
		t.Errorf("updated=%d，期望 2", reply.GetUpdated())
	}
	if repo.unreadBatchCalls.conversationID != 7 {
		t.Errorf("conversation_id=%d，期望 7", repo.unreadBatchCalls.conversationID)
	}
	if len(repo.unreadBatchCalls.userIDs) != 2 || repo.unreadBatchCalls.userIDs[0] != "uid-1" {
		t.Errorf("user_ids 传错了: %v", repo.unreadBatchCalls.userIDs)
	}
	if repo.unreadBatchCalls.delta != 1 {
		t.Errorf("delta=%d，期望 1", repo.unreadBatchCalls.delta)
	}
}
func TestServiceConversation_CheckMembership_PassesThrough(t *testing.T) {
	repo := &fakeConvRepo{isMember: true}
	s := newService(repo)

	reply, err := s.CheckMembership(context.Background(), &pb.CheckMembershipRequest{
		ConversationId: 7,
		UserId:         "uid-42",
	})
	if err != nil {
		t.Fatalf("CheckMembership: %v", err)
	}
	if !reply.GetIsMember() {
		t.Fatal("is_member = false, want true")
	}
}

func TestServiceConversation_ListUnread_ReturnsMap(t *testing.T) {
	repo := &fakeConvRepo{listUnread: map[uint64]uint32{7: 3, 8: 4}}
	s := newService(repo)

	reply, err := s.ListUnread(context.Background(), &pb.ListUnreadRequest{UserId: "uid-42"})
	if err != nil {
		t.Fatalf("ListUnread: %v", err)
	}
	if reply.GetPerConversation()[7] != 3 || reply.GetPerConversation()[8] != 4 {
		t.Fatalf("per = %v, want {7:3, 8:4}", reply.GetPerConversation())
	}
}

func TestServiceConversation_ListConversationMembers_ReturnsIDs(t *testing.T) {
	repo := &fakeConvRepo{conv: &biz.Conversation{ID: 7, MemberIDs: []string{"uid-1", "uid-2", "uid-3"}}}
	s := newService(repo)

	reply, err := s.ListConversationMembers(context.Background(), &pb.ListConversationMembersRequest{ConversationId: 7})
	if err != nil {
		t.Fatalf("ListConversationMembers: %v", err)
	}
	if len(reply.GetMemberIds()) != 3 {
		t.Fatalf("member_ids = %v, want 3 个", reply.GetMemberIds())
	}
}

func TestServiceConversation_ListUserConversations_ReturnsIDs(t *testing.T) {
	repo := &fakeConvRepo{listRet: []*biz.Conversation{{ID: 7}, {ID: 8}}}
	s := newService(repo)

	reply, err := s.ListUserConversations(context.Background(), &pb.ListUserConversationsRequest{UserId: "uid-42"})
	if err != nil {
		t.Fatalf("ListUserConversations: %v", err)
	}
	if len(reply.GetConversationIds()) != 2 {
		t.Fatalf("conversation_ids = %v, want 2 个", reply.GetConversationIds())
	}
}
