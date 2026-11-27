package service

import (
	"context"
	"errors"
	"testing"
	"time"

	pb "chatroom/api/message/service/v1"
	"chatroom/app/message/service/internal/biz"
	"chatroom/pkg/testutil"

	kratoserrors "github.com/go-kratos/kratos/v3/errors"
)

// service 层直持 *biz.MessageUsecase，没有窄接口可 fake —— 替身落在
// biz.MessageRepo 这一层，真实 usecase 建在 fake repo 之上。

// fakeMessageRepo 是 biz.MessageRepo 的内存替身。
type fakeMessageRepo struct {
	// created 是 Create 返回的消息；nil 时按入参构造一条。
	created *biz.Message
	// createErr 是 Create 的错误。
	createErr error

	// listRet / listNext / listErr 是 ListByConversation 的返回值。
	listRet  []*biz.Message
	listNext uint64
	listErr  error

	// sinceRet / sinceLast / sinceMore / sinceErr 是 ListSince 的返回值。
	sinceRet  []*biz.Message
	sinceLast uint64
	sinceMore bool
	sinceErr  error

	// 记录收到的调用参数。
	createCalls struct {
		conversationID uint64
		senderID       string
		typ            biz.MessageType
		content        string
	}
	listCalls struct {
		conversationID uint64
		cursor         uint64
		limit          int
	}
	sinceCalls struct {
		conversationID uint64
		sinceID        uint64
		limit          int
	}
}

func (f *fakeMessageRepo) Create(_ context.Context, m *biz.Message) (*biz.Message, error) {
	f.createCalls.conversationID = m.ConversationID
	f.createCalls.senderID = m.SenderID
	f.createCalls.typ = m.Type
	f.createCalls.content = m.Content
	if f.createErr != nil {
		return nil, f.createErr
	}
	if f.created != nil {
		return f.created, nil
	}
	saved := *m
	saved.ID = 1
	return &saved, nil
}

func (f *fakeMessageRepo) ListByConversation(_ context.Context, conversationID uint64, cursor uint64, limit int) ([]*biz.Message, uint64, error) {
	f.listCalls.conversationID = conversationID
	f.listCalls.cursor = cursor
	f.listCalls.limit = limit
	return f.listRet, f.listNext, f.listErr
}

func (f *fakeMessageRepo) ListSince(_ context.Context, conversationID, sinceID uint64, limit int) ([]*biz.Message, uint64, bool, error) {
	f.sinceCalls.conversationID = conversationID
	f.sinceCalls.sinceID = sinceID
	f.sinceCalls.limit = limit
	return f.sinceRet, f.sinceLast, f.sinceMore, f.sinceErr
}

// fakeMembership 是 biz.ConversationMembershipChecker 的替身。
type fakeMembership struct {
	isMember bool
	err      error
}

func (f fakeMembership) IsMember(context.Context, uint64, string) (bool, error) {
	return f.isMember, f.err
}

// fakePublisher 是 biz.MessagePublisher 的替身，只记录调用。
type fakePublisher struct {
	published []*biz.Message
	err       error
}

func (f *fakePublisher) Publish(_ context.Context, m *biz.Message) error {
	f.published = append(f.published, m)
	return f.err
}

// fakeUnread 是 biz.UnreadCounter 的替身。
type fakeUnread struct {
	// per 是按会话存的未读数。
	per map[uint64]uint32
	// clearErr 是 ClearUnread 的错误。
	clearErr error
	// getErr 是 GetUnread 的错误。
	getErr error
}

func (f *fakeUnread) GetUnread(_ context.Context, conversationID uint64, userID string) (uint32, error) {
	if f.getErr != nil {
		return 0, f.getErr
	}
	return f.per[conversationID], nil
}

func (f *fakeUnread) IncUnread(_ context.Context, conversationID uint64, userID string, delta int32) (uint32, error) {
	if f.per == nil {
		f.per = map[uint64]uint32{}
	}
	f.per[conversationID] += uint32(delta)
	return f.per[conversationID], nil
}

func (f *fakeUnread) IncUnreadBatch(_ context.Context, conversationID uint64, userIDs []string, delta int32) (uint32, error) {
	if f.per == nil {
		f.per = map[uint64]uint32{}
	}
	f.per[conversationID] += uint32(delta) * uint32(len(userIDs))
	return f.per[conversationID], nil
}

func (f *fakeUnread) ClearUnread(_ context.Context, conversationID uint64, userID string) (uint32, error) {
	if f.clearErr != nil {
		return 0, f.clearErr
	}
	if f.per == nil {
		f.per = map[uint64]uint32{}
	}
	f.per[conversationID] = 0
	return 0, nil
}

func (f *fakeUnread) ListUnread(_ context.Context, userID string) (map[uint64]uint32, error) {
	return f.per, nil
}

// fakeMemberLister 是 biz.ConversationMemberLister 的替身。
type fakeMemberLister struct {
	members []string
	err     error
}

func (f fakeMemberLister) ListConversationMembers(context.Context, uint64) ([]string, error) {
	return f.members, f.err
}

// newService 用 fake 造一个真实 usecase + service。t 是测试用例。
// membership 默认放行，unread / lister 默认空。
func newService(repo *fakeMessageRepo) *MessageService {
	return newServiceWith(repo, fakeMembership{isMember: true}, &fakePublisher{}, &fakeUnread{}, fakeMemberLister{})
}

// newServiceWith 同 newService，但自带各协作者替身。t 是测试用例。
func newServiceWith(repo *fakeMessageRepo, membership biz.ConversationMembershipChecker, publisher biz.MessagePublisher, unread biz.UnreadCounter, lister biz.ConversationMemberLister) *MessageService {
	uc := biz.NewMessageUsecase(repo, membership, publisher, unread, lister, biz.StorageOptions{}, nil)
	return NewMessageService(uc)
}

// msgTestSecret 测试用的 JWT 密钥。
const msgTestSecret = "test-secret-32-bytes-long-xxxxxx"

// convAuthedCtx 用一次 auth.Server 中间件把 uid 注入 ctx。
func convAuthedCtx(t *testing.T, uid string) context.Context {
	t.Helper()
	return testutil.AuthedCtxWithToken(t, testutil.JWTFor(t, msgTestSecret, uid), msgTestSecret)
}
func TestServiceMessage_Send_NilRequest(t *testing.T) {
	s := newService(&fakeMessageRepo{})
	if _, err := s.SendMessage(context.Background(), nil); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceMessage_Send_ZeroConversationID(t *testing.T) {
	s := newService(&fakeMessageRepo{})
	ctx := convAuthedCtx(t, "uid-1")
	if _, err := s.SendMessage(ctx, &pb.SendMessageRequest{}); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceMessage_Send_NoAuthUID(t *testing.T) {
	s := newService(&fakeMessageRepo{})
	_, err := s.SendMessage(context.Background(), &pb.SendMessageRequest{ConversationId: 7})
	if !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceMessage_Send_UnknownType(t *testing.T) {
	s := newService(&fakeMessageRepo{})
	ctx := convAuthedCtx(t, "uid-1")
	_, err := s.SendMessage(ctx, &pb.SendMessageRequest{
		ConversationId: 7,
		Type:           pb.MessageType(99),
		Content:        "hi",
	})
	if !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceMessage_Send_PassesCallerAndFields(t *testing.T) {
	repo := &fakeMessageRepo{created: &biz.Message{
		ID: 11, ConversationID: 7, SenderID: "uid-42",
		Type: biz.MessageTypeText, Content: "hello",
	}}
	s := newService(repo)
	ctx := convAuthedCtx(t, "uid-42")

	reply, err := s.SendMessage(ctx, &pb.SendMessageRequest{
		ConversationId: 7,
		Type:           pb.MessageType_MESSAGE_TYPE_TEXT,
		Content:        "hello",
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	// caller uid 必须来自 ctx，不能从请求里取。
	if repo.createCalls.senderID != "uid-42" {
		t.Errorf("caller id = %q, want uid-42", repo.createCalls.senderID)
	}
	if repo.createCalls.conversationID != 7 {
		t.Errorf("conversation id = %d, want 7", repo.createCalls.conversationID)
	}
	if repo.createCalls.typ != biz.MessageTypeText {
		t.Errorf("type = %v, want MessageTypeText", repo.createCalls.typ)
	}
	if repo.createCalls.content != "hello" {
		t.Errorf("content = %q, want %q", repo.createCalls.content, "hello")
	}
	if reply.GetMessage().GetId() != 11 {
		t.Errorf("reply message id = %d, want 11", reply.GetMessage().GetId())
	}
	if reply.GetMessage().GetType() != pb.MessageType_MESSAGE_TYPE_TEXT {
		t.Errorf("reply type = %v, want TEXT", reply.GetMessage().GetType())
	}
}

func TestServiceMessage_Send_ImageTypeMaps(t *testing.T) {
	repo := &fakeMessageRepo{created: &biz.Message{ID: 1, Type: biz.MessageTypeImage}}
	s := newService(repo)
	ctx := convAuthedCtx(t, "uid-1")

	// 图片消息的 content 要过对象存储校验 —— 没配存储时直接拒，
	// 消息压根不落库。类型映射的正确性由 biz 层的用例覆盖。
	_, err := s.SendMessage(ctx, &pb.SendMessageRequest{
		ConversationId: 7,
		Type:           pb.MessageType_MESSAGE_TYPE_IMAGE,
		Content:        "http://img",
	})
	if !errors.Is(err, biz.ErrStorageNotConfigured) {
		t.Fatalf("error = %v, want %v", err, biz.ErrStorageNotConfigured)
	}
	if repo.createCalls.typ != 0 {
		t.Errorf("biz type = %v, 图片校验没过就不该落库", repo.createCalls.typ)
	}
}

func TestServiceMessage_Send_EmojiTypeMaps(t *testing.T) {
	repo := &fakeMessageRepo{created: &biz.Message{ID: 1, Type: biz.MessageTypeEmoji}}
	s := newService(repo)
	ctx := convAuthedCtx(t, "uid-1")

	reply, err := s.SendMessage(ctx, &pb.SendMessageRequest{
		ConversationId: 7,
		Type:           pb.MessageType_MESSAGE_TYPE_EMOJI,
		Content:        "😀",
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if repo.createCalls.typ != biz.MessageTypeEmoji {
		t.Errorf("biz type = %v, want MessageTypeEmoji", repo.createCalls.typ)
	}
	if reply.GetMessage().GetType() != pb.MessageType_MESSAGE_TYPE_EMOJI {
		t.Errorf("reply type = %v, want EMOJI", reply.GetMessage().GetType())
	}
}

func TestServiceMessage_Send_PropagatesUsecaseError(t *testing.T) {
	// caller 不在会里 → biz 返 ErrMessageNotMember。
	s := newServiceWith(&fakeMessageRepo{}, fakeMembership{isMember: false}, &fakePublisher{}, &fakeUnread{}, fakeMemberLister{})
	ctx := convAuthedCtx(t, "uid-42")

	_, err := s.SendMessage(ctx, &pb.SendMessageRequest{ConversationId: 7, Content: "hi"})
	if !errors.Is(err, biz.ErrMessageNotMember) {
		t.Fatalf("error = %v, want %v", err, biz.ErrMessageNotMember)
	}
}

func TestServiceMessage_Send_PublishesAfterCreate(t *testing.T) {
	// 落库成功后广播 —— 顺序反了客户端会收到一条查不到的消息。
	repo := &fakeMessageRepo{created: &biz.Message{ID: 11, ConversationID: 7, Type: biz.MessageTypeText}}
	publisher := &fakePublisher{}
	s := newServiceWith(repo, fakeMembership{isMember: true}, publisher, &fakeUnread{}, fakeMemberLister{})
	ctx := convAuthedCtx(t, "uid-42")

	if _, err := s.SendMessage(ctx, &pb.SendMessageRequest{ConversationId: 7, Content: "hi"}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if len(publisher.published) != 1 {
		t.Fatalf("published = %d, want 1", len(publisher.published))
	}
	if publisher.published[0].ID != 11 {
		t.Fatalf("published id = %d, want 11", publisher.published[0].ID)
	}
}
func TestServiceMessage_List_NilRequest(t *testing.T) {
	s := newService(&fakeMessageRepo{})
	if _, err := s.ListMessages(context.Background(), nil); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceMessage_List_ZeroConversationID(t *testing.T) {
	s := newService(&fakeMessageRepo{})
	ctx := convAuthedCtx(t, "uid-1")
	if _, err := s.ListMessages(ctx, &pb.ListMessagesRequest{}); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceMessage_List_NoAuthUID(t *testing.T) {
	s := newService(&fakeMessageRepo{})
	_, err := s.ListMessages(context.Background(), &pb.ListMessagesRequest{ConversationId: 7})
	if !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceMessage_List_PassesCursorAndPageSize(t *testing.T) {
	repo := &fakeMessageRepo{
		listRet: []*biz.Message{
			{ID: 2, ConversationID: 7, SenderID: "uid-42", Type: biz.MessageTypeText, Content: "b"},
			{ID: 1, ConversationID: 7, SenderID: "uid-42", Type: biz.MessageTypeText, Content: "a"},
		},
		listNext: 1,
	}
	s := newService(repo)
	ctx := convAuthedCtx(t, "uid-42")

	reply, err := s.ListMessages(ctx, &pb.ListMessagesRequest{
		ConversationId: 7,
		Cursor:         5,
		PageSize:       20,
	})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if repo.listCalls.cursor != 5 {
		t.Errorf("cursor = %d, want 5", repo.listCalls.cursor)
	}
	if repo.listCalls.limit != 20 {
		t.Errorf("limit = %d, want 20", repo.listCalls.limit)
	}
	if repo.listCalls.conversationID != 7 {
		t.Errorf("conversation id = %d, want 7", repo.listCalls.conversationID)
	}
	if len(reply.GetMessages()) != 2 {
		t.Fatalf("消息数 = %d, want 2", len(reply.GetMessages()))
	}
	if reply.GetNextCursor() != 1 {
		t.Errorf("next cursor = %d, want 1", reply.GetNextCursor())
	}
	// 顺序要跟 biz 给的一致（新的在前）。
	if reply.GetMessages()[0].GetId() != 2 {
		t.Errorf("首条 id = %d, want 2", reply.GetMessages()[0].GetId())
	}
}

func TestServiceMessage_List_EmptyResultIsNotNil(t *testing.T) {
	s := newService(&fakeMessageRepo{})
	ctx := convAuthedCtx(t, "uid-42")

	reply, err := s.ListMessages(ctx, &pb.ListMessagesRequest{ConversationId: 7, PageSize: 10})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if reply.GetMessages() == nil {
		t.Error("空结果不该是 nil slice")
	}
}
func TestServiceMessage_Since_NilRequest(t *testing.T) {
	s := newService(&fakeMessageRepo{})
	if _, err := s.ListMessagesSince(context.Background(), nil); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceMessage_Since_ZeroConversationID(t *testing.T) {
	s := newService(&fakeMessageRepo{})
	ctx := convAuthedCtx(t, "uid-1")
	if _, err := s.ListMessagesSince(ctx, &pb.ListMessagesSinceRequest{}); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceMessage_Since_NoAuthUID(t *testing.T) {
	s := newService(&fakeMessageRepo{})
	_, err := s.ListMessagesSince(context.Background(), &pb.ListMessagesSinceRequest{ConversationId: 7})
	if !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceMessage_Since_PassesSinceIDAndMapsReply(t *testing.T) {
	repo := &fakeMessageRepo{
		sinceRet: []*biz.Message{
			{ID: 11, ConversationID: 7, SenderID: "uid-42", Type: biz.MessageTypeText, Content: "a"},
			{ID: 12, ConversationID: 7, SenderID: "uid-43", Type: biz.MessageTypeImage, Content: "http://x/y.png"},
		},
		sinceLast: 12,
		sinceMore: true,
	}
	s := newService(repo)
	ctx := convAuthedCtx(t, "uid-42")

	reply, err := s.ListMessagesSince(ctx, &pb.ListMessagesSinceRequest{
		ConversationId: 7,
		SinceId:        10,
		PageSize:       50,
	})
	if err != nil {
		t.Fatalf("ListMessagesSince: %v", err)
	}
	if repo.sinceCalls.sinceID != 10 {
		t.Errorf("since_id = %d, want 10", repo.sinceCalls.sinceID)
	}
	if repo.sinceCalls.limit != 50 {
		t.Errorf("limit = %d, want 50", repo.sinceCalls.limit)
	}
	if repo.sinceCalls.conversationID != 7 {
		t.Errorf("conversation id = %d, want 7", repo.sinceCalls.conversationID)
	}
	if len(reply.GetMessages()) != 2 {
		t.Fatalf("消息数 = %d, want 2", len(reply.GetMessages()))
	}
	if reply.GetLastMessageId() != 12 {
		t.Errorf("last_message_id = %d, want 12", reply.GetLastMessageId())
	}
	if !reply.GetHasMore() {
		t.Error("has_more = false, want true")
	}
	// 顺序跟 biz 给的一致（正序：旧的在前）。
	if reply.GetMessages()[0].GetId() != 11 {
		t.Errorf("首条 id = %d, want 11", reply.GetMessages()[0].GetId())
	}
}
func TestServiceMessage_MarkRead_NilRequest(t *testing.T) {
	s := newService(&fakeMessageRepo{})
	if _, err := s.MarkRead(context.Background(), nil); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceMessage_MarkRead_ZeroConversationID(t *testing.T) {
	s := newService(&fakeMessageRepo{})
	ctx := convAuthedCtx(t, "uid-1")
	if _, err := s.MarkRead(ctx, &pb.MarkReadRequest{}); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceMessage_MarkRead_NoAuthUID(t *testing.T) {
	s := newService(&fakeMessageRepo{})
	_, err := s.MarkRead(context.Background(), &pb.MarkReadRequest{ConversationId: 7})
	if !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceMessage_MarkRead_ReturnsCount(t *testing.T) {
	repo := &fakeMessageRepo{}
	s := newService(repo)
	ctx := convAuthedCtx(t, "uid-42")

	reply, err := s.MarkRead(ctx, &pb.MarkReadRequest{ConversationId: 7})
	if err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if reply.GetUnreadCount() != 0 {
		t.Errorf("unread count = %d, want 0", reply.GetUnreadCount())
	}
}
func TestServiceMessage_GetUnreadCount_NilRequest(t *testing.T) {
	s := newService(&fakeMessageRepo{})
	if _, err := s.GetUnreadCount(context.Background(), nil); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceMessage_GetUnreadCount_NoAuthUID(t *testing.T) {
	s := newService(&fakeMessageRepo{})
	_, err := s.GetUnreadCount(context.Background(), &pb.GetUnreadCountRequest{})
	if !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceMessage_GetUnreadCount_AllConversations(t *testing.T) {
	unread := &fakeUnread{per: map[uint64]uint32{7: 3, 8: 4}}
	s := newServiceWith(&fakeMessageRepo{}, fakeMembership{isMember: true}, &fakePublisher{}, unread, fakeMemberLister{})
	ctx := convAuthedCtx(t, "uid-42")

	reply, err := s.GetUnreadCount(ctx, &pb.GetUnreadCountRequest{})
	if err != nil {
		t.Fatalf("GetUnreadCount: %v", err)
	}
	if reply.GetUnreadCount() != 7 {
		t.Errorf("总数 = %d, want 7", reply.GetUnreadCount())
	}
	if reply.GetPerConversation()[7] != 3 || reply.GetPerConversation()[8] != 4 {
		t.Errorf("per = %v, want {7:3, 8:4}", reply.GetPerConversation())
	}
}

func TestServiceMessage_GetUnreadCount_SingleConversation(t *testing.T) {
	unread := &fakeUnread{per: map[uint64]uint32{7: 3}}
	s := newServiceWith(&fakeMessageRepo{}, fakeMembership{isMember: true}, &fakePublisher{}, unread, fakeMemberLister{})
	ctx := convAuthedCtx(t, "uid-42")

	reply, err := s.GetUnreadCount(ctx, &pb.GetUnreadCountRequest{ConversationId: 7})
	if err != nil {
		t.Fatalf("GetUnreadCount: %v", err)
	}
	if reply.GetUnreadCount() != 3 {
		t.Errorf("未读数 = %d, want 3", reply.GetUnreadCount())
	}
}
func TestConvertMessageInfo_MapsAllFields(t *testing.T) {
	in := &biz.Message{
		ID:             9,
		ConversationID: 7,
		SenderID:       "uid-42",
		Type:           biz.MessageTypeImage,
		Content:        "img",
		CreatedAt:      time.Unix(1700000000, 0),
	}
	out := convertMessageInfo(in)
	if out.GetId() != 9 {
		t.Errorf("id = %d, want 9", out.GetId())
	}
	if out.GetConversationId() != 7 {
		t.Errorf("conversation id = %d, want 7", out.GetConversationId())
	}
	if out.GetSenderId() != "uid-42" {
		t.Errorf("sender id = %q, want uid-42", out.GetSenderId())
	}
	if out.GetType() != pb.MessageType_MESSAGE_TYPE_IMAGE {
		t.Errorf("type = %v, want IMAGE", out.GetType())
	}
	if out.GetContent() != "img" {
		t.Errorf("content = %q, want %q", out.GetContent(), "img")
	}
	if out.GetCreatedAt() != 1700000000 {
		t.Errorf("created at = %d, want 1700000000", out.GetCreatedAt())
	}
}

func TestConvertMessageInfo_NilReturnsNil(t *testing.T) {
	if convertMessageInfo(nil) != nil {
		t.Error("nil 输入该返 nil")
	}
}
