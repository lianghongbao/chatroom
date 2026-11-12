package biz

import (
	"context"
	"errors"
	"testing"

	kratoserrors "github.com/go-kratos/kratos/v3/errors"
)

// fakeMessageRepo 是 biz.MessageRepo 的内存替身。
type fakeMessageRepo struct {
	// nextID 自增 id。
	nextID uint64
	// messages 存的消息。key 是会话 id。
	messages map[uint64][]*Message
	// createErr 落库时返回的错误。
	createErr error
	// listErr 拉历史时返回的错误。
	listErr error
	// createCalls 落库次数。
	createCalls int
}

func newFakeMessageRepo() *fakeMessageRepo {
	return &fakeMessageRepo{nextID: 1, messages: make(map[uint64][]*Message)}
}

func (r *fakeMessageRepo) Create(_ context.Context, m *Message) (*Message, error) {
	r.createCalls++
	if r.createErr != nil {
		return nil, r.createErr
	}
	saved := *m
	saved.ID = r.nextID
	r.nextID++
	r.messages[m.ConversationID] = append(r.messages[m.ConversationID], &saved)
	return &saved, nil
}

func (r *fakeMessageRepo) ListByConversation(_ context.Context, conversationID uint64, cursor uint64, limit int) ([]*Message, uint64, error) {
	if r.listErr != nil {
		return nil, 0, r.listErr
	}
	all := r.messages[conversationID]
	out := make([]*Message, 0, len(all))
	for _, m := range all {
		if cursor > 0 && m.ID >= cursor {
			continue
		}
		out = append(out, m)
	}
	// 倒序（新的在前）。
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	var next uint64
	if len(out) > limit {
		next = out[limit-1].ID
		out = out[:limit]
	}
	return out, next, nil
}

// ListSince 拉「id 严格大于 sinceID」的消息，按 id 正序。
func (r *fakeMessageRepo) ListSince(_ context.Context, conversationID, sinceID uint64, limit int) ([]*Message, uint64, bool, error) {
	if r.listErr != nil {
		return nil, 0, false, r.listErr
	}
	all := r.messages[conversationID]
	out := make([]*Message, 0, len(all))
	for _, m := range all {
		if m.ID <= sinceID {
			continue
		}
		out = append(out, m)
	}
	// 正序（旧的在前）—— fake 里存的就是正序，不用排。
	var last uint64
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	for _, m := range out {
		last = m.ID
	}
	return out, last, hasMore, nil
}

// fakeMembershipChecker 是 biz.ConversationMembershipChecker 的内存替身。
type fakeMembershipChecker struct {
	// members 是「会话 id → 成员集合」。
	members map[uint64]map[string]struct{}
	// err 模拟 conversation 服务不可达。
	err error
	// calls 调用次数。
	calls int
}

func newFakeMembershipChecker() *fakeMembershipChecker {
	return &fakeMembershipChecker{members: make(map[uint64]map[string]struct{})}
}

func (f *fakeMembershipChecker) add(conversationID uint64, userID string) {
	if f.members[conversationID] == nil {
		f.members[conversationID] = make(map[string]struct{})
	}
	f.members[conversationID][userID] = struct{}{}
}

func (f *fakeMembershipChecker) IsMember(_ context.Context, conversationID uint64, userID string) (bool, error) {
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	set, ok := f.members[conversationID]
	if !ok {
		return false, nil
	}
	_, member := set[userID]
	return member, nil
}

// fakePublisher 是 biz.MessagePublisher 的内存替身。
type fakePublisher struct {
	// published 收到的消息。
	published []*Message
	// err 模拟广播失败。
	err error
}

func (f *fakePublisher) Publish(_ context.Context, m *Message) error {
	if f.err != nil {
		return f.err
	}
	f.published = append(f.published, m)
	return nil
}

// fakeUnreadCounter 是 biz.UnreadCounter 的内存替身。
type fakeUnreadCounter struct {
	// counts 存各会话的未读数。
	counts map[uint64]uint32
	// err 模拟下游失败。
	err error
	// clearCalls 清零次数。
	clearCalls int
	// incCalls 增加未读的次数。
	incCalls int
	// incUsers 记录给谁加过未读。
	incUsers []string
	// batchIncCalls 批量增加未读的次数。
	batchIncCalls int
	// batchIncUsers 记录批量给谁加过未读。
	batchIncUsers []string
	// batchErr 模拟批量端点失败（单个端点仍可用）。
	batchErr error
}

// errBatchFailed 批量端点失败的哨兵错误。
var errBatchFailed = errors.New("batch update failed")

func newFakeUnreadCounter() *fakeUnreadCounter {
	return &fakeUnreadCounter{counts: make(map[uint64]uint32)}
}

func (f *fakeUnreadCounter) GetUnread(_ context.Context, conversationID uint64, _ string) (uint32, error) {
	if f.err != nil {
		return 0, f.err
	}
	return f.counts[conversationID], nil
}

func (f *fakeUnreadCounter) IncUnread(_ context.Context, conversationID uint64, userID string, delta int32) (uint32, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.incCalls++
	f.incUsers = append(f.incUsers, userID)
	n := int32(f.counts[conversationID]) + delta
	if n < 0 {
		n = 0
	}
	f.counts[conversationID] = uint32(n)
	return f.counts[conversationID], nil
}

func (f *fakeUnreadCounter) IncUnreadBatch(_ context.Context, conversationID uint64, userIDs []string, delta int32) (uint32, error) {
	if f.batchErr != nil {
		return 0, f.batchErr
	}
	if f.err != nil {
		return 0, f.err
	}
	f.batchIncCalls++
	f.batchIncUsers = append(f.batchIncUsers, userIDs...)
	n := int32(f.counts[conversationID]) + delta*int32(len(userIDs))
	if n < 0 {
		n = 0
	}
	f.counts[conversationID] = uint32(n)
	return uint32(len(userIDs)), nil
}

func (f *fakeUnreadCounter) ClearUnread(_ context.Context, conversationID uint64, _ string) (uint32, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.clearCalls++
	f.counts[conversationID] = 0
	return 0, nil
}

func (f *fakeUnreadCounter) ListUnread(_ context.Context, _ string) (map[uint64]uint32, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[uint64]uint32)
	for k, v := range f.counts {
		if v > 0 {
			out[k] = v
		}
	}
	return out, nil
}

// fakeMemberLister 是 biz.ConversationMemberLister 的内存替身。
type fakeMemberLister struct {
	// members 会话 id → 成员列表。
	members map[uint64][]string
	// err 模拟 conversation 服务不可达。
	err error
	// calls 调用次数。
	calls int
}

func newFakeMemberLister() *fakeMemberLister {
	return &fakeMemberLister{members: make(map[uint64][]string)}
}

func (f *fakeMemberLister) ListConversationMembers(_ context.Context, conversationID uint64) ([]string, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.members[conversationID], nil
}

// newTestMessageUsecase 构造被测用例。
func newTestMessageUsecase() (*MessageUsecase, *fakeMessageRepo, *fakeMembershipChecker, *fakePublisher, *fakeUnreadCounter) {
	repo := newFakeMessageRepo()
	membership := newFakeMembershipChecker()
	publisher := &fakePublisher{}
	unread := newFakeUnreadCounter()
	uc := NewMessageUsecase(repo, membership, publisher, unread, nil, nil)
	return uc, repo, membership, publisher, unread
}

// newTestMessageUsecaseWithLister 构造带成员列表能力的被测用例。
func newTestMessageUsecaseWithLister() (*MessageUsecase, *fakeMembershipChecker, *fakeUnreadCounter, *fakeMemberLister) {
	repo := newFakeMessageRepo()
	membership := newFakeMembershipChecker()
	publisher := &fakePublisher{}
	unread := newFakeUnreadCounter()
	lister := newFakeMemberLister()
	uc := NewMessageUsecase(repo, membership, publisher, unread, lister, nil)
	return uc, membership, unread, lister
}

func TestMessage_SendMessageStoresAndPublishes(t *testing.T) {
	uc, repo, membership, publisher, _ := newTestMessageUsecase()
	membership.add(7, "uid-42")

	msg, err := uc.SendMessage(context.Background(), "uid-42", 7, MessageTypeText, "hello")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if msg.ID == 0 {
		t.Error("落库后应有 id")
	}
	if msg.Content != "hello" {
		t.Errorf("content = %q, want %q", msg.Content, "hello")
	}
	if repo.createCalls != 1 {
		t.Errorf("落库次数 = %d, want 1", repo.createCalls)
	}
	// 广播必须在落库之后 —— 客户端收到推送时消息要已经存下来了。
	if len(publisher.published) != 1 {
		t.Fatalf("广播次数 = %d, want 1", len(publisher.published))
	}
	if publisher.published[0].ID != msg.ID {
		t.Errorf("广播的 message id = %d, want %d", publisher.published[0].ID, msg.ID)
	}
}

func TestMessage_SendMessageRejectsNonMember(t *testing.T) {
	uc, repo, membership, publisher, _ := newTestMessageUsecase()
	membership.add(7, "uid-42")

	// 99 不在会话里。
	_, err := uc.SendMessage(context.Background(), "uid-99", 7, MessageTypeText, "hi")
	if !errors.Is(err, ErrMessageNotMember) {
		t.Fatalf("err = %v, want ErrMessageNotMember", err)
	}
	if repo.createCalls != 0 {
		t.Error("非成员不该落库")
	}
	if len(publisher.published) != 0 {
		t.Error("非成员不该广播")
	}
}

// 会话服务不可达时必须**拒绝**，不能放行 —— fail open 等于谁都能往
// 别人的会话里发消息。
func TestMessage_SendMessageRejectsWhenConversationServiceDown(t *testing.T) {
	uc, repo, membership, _, _ := newTestMessageUsecase()
	membership.err = errors.New("conversation service down")

	_, err := uc.SendMessage(context.Background(), "uid-42", 7, MessageTypeText, "hi")
	if !errors.Is(err, ErrMessageConversationUnavailable) {
		t.Fatalf("err = %v, want ErrMessageConversationUnavailable", err)
	}
	if repo.createCalls != 0 {
		t.Error("下游不可达时不该落库")
	}
}

func TestMessage_SendMessageRejectsEmptyContent(t *testing.T) {
	uc, _, membership, _, _ := newTestMessageUsecase()
	membership.add(7, "uid-42")

	_, err := uc.SendMessage(context.Background(), "uid-42", 7, MessageTypeText, "")
	if !errors.Is(err, ErrMessageInvalidArgument) {
		t.Fatalf("err = %v, want ErrMessageInvalidArgument", err)
	}
}

func TestMessage_SendMessageRejectsTooLongContent(t *testing.T) {
	uc, _, membership, _, _ := newTestMessageUsecase()
	membership.add(7, "uid-42")

	long := make([]rune, MaxMessageContentLen+1)
	for i := range long {
		long[i] = 'a'
	}
	_, err := uc.SendMessage(context.Background(), "uid-42", 7, MessageTypeText, string(long))
	if !errors.Is(err, ErrMessageContentTooLong) {
		t.Fatalf("err = %v, want ErrMessageContentTooLong", err)
	}
}

// 广播失败不能影响落库结果 —— 消息已经存下来了，客户端拉历史能拿到。
func TestMessage_SendMessageSucceedsWhenPublishFails(t *testing.T) {
	uc, repo, membership, publisher, _ := newTestMessageUsecase()
	membership.add(7, "uid-42")
	publisher.err = errors.New("redis down")

	msg, err := uc.SendMessage(context.Background(), "uid-42", 7, MessageTypeText, "hello")
	if err != nil {
		t.Fatalf("广播失败不该让发消息失败: %v", err)
	}
	if msg.ID == 0 {
		t.Error("落库后应有 id")
	}
	if repo.createCalls != 1 {
		t.Error("消息应该已落库")
	}
}

func TestMessage_ListMessagesRejectsNonMember(t *testing.T) {
	uc, _, membership, _, _ := newTestMessageUsecase()
	membership.add(7, "uid-42")

	_, _, err := uc.ListMessages(context.Background(), "uid-99", 7, 0, 10)
	if !errors.Is(err, ErrMessageNotMember) {
		t.Fatalf("err = %v, want ErrMessageNotMember", err)
	}
}

func TestMessage_ListMessagesPaginates(t *testing.T) {
	uc, _, membership, _, _ := newTestMessageUsecase()
	membership.add(7, "uid-42")

	for i := 0; i < 5; i++ {
		if _, err := uc.SendMessage(context.Background(), "uid-42", 7, MessageTypeText, "m"); err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
	}

	msgs, next, err := uc.ListMessages(context.Background(), "uid-42", 7, 0, 3)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("第一页条数 = %d, want 3", len(msgs))
	}
	if next == 0 {
		t.Fatal("还有更多时 next cursor 不该是 0")
	}

	msgs2, next2, err := uc.ListMessages(context.Background(), "uid-42", 7, next, 3)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs2) != 2 {
		t.Fatalf("第二页条数 = %d, want 2", len(msgs2))
	}
	if next2 != 0 {
		t.Errorf("末页 next cursor = %d, want 0", next2)
	}
}

// TestMessage_ListMessagesSinceRejectsNonMember 非成员不能补消息 ——
// 补消息也是读路径，成员身份校验不能漏。
func TestMessage_ListMessagesSinceRejectsNonMember(t *testing.T) {
	uc, _, membership, _, _ := newTestMessageUsecase()
	membership.add(7, "uid-42")

	_, _, _, err := uc.ListMessagesSince(context.Background(), "uid-99", 7, 0, 10)
	if !errors.Is(err, ErrMessageNotMember) {
		t.Fatalf("err = %v, want ErrMessageNotMember", err)
	}
}

// TestMessage_ListMessagesSinceRejectsZeroIDs 参数非法要报错。
func TestMessage_ListMessagesSinceRejectsZeroIDs(t *testing.T) {
	uc, _, membership, _, _ := newTestMessageUsecase()
	membership.add(7, "uid-42")

	if _, _, _, err := uc.ListMessagesSince(context.Background(), "", 7, 0, 10); !errors.Is(err, ErrMessageInvalidArgument) {
		t.Errorf("callerID empty err = %v, want ErrMessageInvalidArgument", err)
	}
	if _, _, _, err := uc.ListMessagesSince(context.Background(), "uid-42", 0, 0, 10); !errors.Is(err, ErrMessageInvalidArgument) {
		t.Errorf("conversationID=0 err = %v, want ErrMessageInvalidArgument", err)
	}
}

// TestMessage_ListMessagesSinceReturnsNewer ListSince 只返回 id 更大的消息。
func TestMessage_ListMessagesSinceReturnsNewer(t *testing.T) {
	uc, _, membership, _, _ := newTestMessageUsecase()
	membership.add(7, "uid-42")

	var ids []uint64
	for i := 0; i < 3; i++ {
		m, err := uc.SendMessage(context.Background(), "uid-42", 7, MessageTypeText, "m")
		if err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
		ids = append(ids, m.ID)
	}

	msgs, last, hasMore, err := uc.ListMessagesSince(context.Background(), "uid-42", 7, ids[0], 10)
	if err != nil {
		t.Fatalf("ListMessagesSince: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("条数 = %d, want 2", len(msgs))
	}
	if msgs[0].ID != ids[1] {
		t.Errorf("第一条 id = %d, want %d", msgs[0].ID, ids[1])
	}
	if last != ids[2] {
		t.Errorf("last = %d, want %d", last, ids[2])
	}
	if hasMore {
		t.Error("has_more = true, want false")
	}
}

// TestMessage_ListMessagesSinceFailsClosed 成员校验能力缺失要拒绝 ——
// 不能让补消息变成绕过鉴权的通道。
func TestMessage_ListMessagesSinceFailsClosed(t *testing.T) {
	uc, _, _, _, _ := newTestMessageUsecase()
	uc.membership = nil

	_, _, _, err := uc.ListMessagesSince(context.Background(), "uid-42", 7, 0, 10)
	if !errors.Is(err, ErrMessageConversationUnavailable) {
		t.Fatalf("err = %v, want ErrMessageConversationUnavailable", err)
	}
}

func TestMessage_MarkReadClearsUnread(t *testing.T) {
	uc, _, membership, _, unread := newTestMessageUsecase()
	membership.add(7, "uid-42")
	unread.counts[7] = 5

	n, err := uc.MarkRead(context.Background(), "uid-42", 7)
	if err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if n != 0 {
		t.Errorf("清零后未读数 = %d, want 0", n)
	}
	if unread.clearCalls != 1 {
		t.Errorf("清零次数 = %d, want 1", unread.clearCalls)
	}
}

func TestMessage_MarkReadRejectsNonMember(t *testing.T) {
	uc, _, membership, _, unread := newTestMessageUsecase()
	membership.add(7, "uid-42")
	unread.counts[7] = 5

	_, err := uc.MarkRead(context.Background(), "uid-99", 7)
	if !errors.Is(err, ErrMessageNotMember) {
		t.Fatalf("err = %v, want ErrMessageNotMember", err)
	}
	if unread.clearCalls != 0 {
		t.Error("非成员不该能清未读数")
	}
}

func TestMessage_GetUnreadCountAll(t *testing.T) {
	uc, _, _, _, unread := newTestMessageUsecase()
	unread.counts[7] = 3
	unread.counts[8] = 4

	total, per, err := uc.GetUnreadCount(context.Background(), "uid-42", 0)
	if err != nil {
		t.Fatalf("GetUnreadCount: %v", err)
	}
	if total != 7 {
		t.Errorf("总数 = %d, want 7", total)
	}
	if per[7] != 3 || per[8] != 4 {
		t.Errorf("per = %v, want {7:3, 8:4}", per)
	}
}

func TestMessage_GetUnreadCountSingleConversation(t *testing.T) {
	uc, _, membership, _, unread := newTestMessageUsecase()
	membership.add(7, "uid-42")
	unread.counts[7] = 3

	total, per, err := uc.GetUnreadCount(context.Background(), "uid-42", 7)
	if err != nil {
		t.Fatalf("GetUnreadCount: %v", err)
	}
	if total != 3 {
		t.Errorf("总数 = %d, want 3", total)
	}
	if per != nil {
		t.Errorf("单会话查询不该返回 map: %v", per)
	}
}

// 查单个会话的未读数也要先确认在会里 —— 否则能改 conversation_id
// 去探测别人会话的未读数。
func TestMessage_GetUnreadCountSingleConversationRejectsNonMember(t *testing.T) {
	uc, _, membership, _, unread := newTestMessageUsecase()
	membership.add(7, "uid-42")
	unread.counts[7] = 3

	// 99 不是 7 的成员
	if _, _, err := uc.GetUnreadCount(context.Background(), "uid-99", 7); err == nil {
		t.Fatal("非成员查未读数应该被拒")
	}
}

// 发消息要给会话里其它成员加未读 —— 收消息的人得看到红点。
//
// 用批量端点一次加完 —— 逐个调是 N 次跨进程调用（500 人的群 = 500 次往返）。
func TestMessage_SendMessageIncreasesUnreadForOtherMembers(t *testing.T) {
	uc, membership, unread, lister := newTestMessageUsecaseWithLister()
	membership.add(7, "uid-42")
	lister.members[7] = []string{"uid-42", "uid-99", "uid-100"}

	if _, err := uc.SendMessage(context.Background(), "uid-42", 7, MessageTypeText, "hi"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	if unread.batchIncCalls != 1 {
		t.Fatalf("批量加未读次数 = %d, want 1", unread.batchIncCalls)
	}
	if unread.incCalls != 0 {
		t.Errorf("不该逐个加未读（%d 次）—— 批量端点失败才退回", unread.incCalls)
	}
	if len(unread.batchIncUsers) != 2 {
		t.Fatalf("批量加未读的人数 = %d, want 2 (排除发送方)", len(unread.batchIncUsers))
	}
	for _, uid := range unread.batchIncUsers {
		if uid == "uid-42" {
			t.Error("发送方不该给自己加未读")
		}
	}
	if unread.counts[7] != 2 {
		t.Errorf("未读数 = %d, want 2", unread.counts[7])
	}
}

// 批量端点失败时要退回逐个加 —— 不能因为批量失败就让所有人都收不到红点。
func TestMessage_SendMessageFallsBackWhenBatchFails(t *testing.T) {
	uc, membership, unread, lister := newTestMessageUsecaseWithLister()
	membership.add(7, "uid-42")
	lister.members[7] = []string{"uid-42", "uid-99", "uid-100"}
	// 批量失败，但单个能用
	unread.batchErr = errBatchFailed
	unread.err = nil

	if _, err := uc.SendMessage(context.Background(), "uid-42", 7, MessageTypeText, "hi"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	if unread.incCalls != 2 {
		t.Errorf("退回逐个加的次数 = %d, want 2", unread.incCalls)
	}
	if unread.counts[7] != 2 {
		t.Errorf("未读数 = %d, want 2", unread.counts[7])
	}
}

// 连续发多条，未读数要累加。
func TestMessage_SendMessageAccumulatesUnread(t *testing.T) {
	uc, membership, unread, lister := newTestMessageUsecaseWithLister()
	membership.add(7, "uid-42")
	lister.members[7] = []string{"uid-42", "uid-99"}

	for i := 0; i < 3; i++ {
		if _, err := uc.SendMessage(context.Background(), "uid-42", 7, MessageTypeText, "m"); err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
	}
	if unread.counts[7] != 3 {
		t.Errorf("未读数 = %d, want 3", unread.counts[7])
	}
}

// 加未读失败不能把发消息搞失败 —— 消息已经落库了。
func TestMessage_SendMessageSucceedsWhenUnreadUpdateFails(t *testing.T) {
	uc, membership, unread, lister := newTestMessageUsecaseWithLister()
	membership.add(7, "uid-42")
	lister.members[7] = []string{"uid-42", "uid-99"}
	unread.err = errors.New("conversation service down")

	msg, err := uc.SendMessage(context.Background(), "uid-42", 7, MessageTypeText, "hi")
	if err != nil {
		t.Fatalf("加未读失败不该让发消息失败: %v", err)
	}
	if msg.ID == 0 {
		t.Error("消息应该已落库")
	}
}

// 查成员失败也不能让发消息失败。
func TestMessage_SendMessageSucceedsWhenMemberListFails(t *testing.T) {
	uc, membership, unread, lister := newTestMessageUsecaseWithLister()
	membership.add(7, "uid-42")
	lister.err = errors.New("conversation service down")

	if _, err := uc.SendMessage(context.Background(), "uid-42", 7, MessageTypeText, "hi"); err != nil {
		t.Fatalf("查成员失败不该让发消息失败: %v", err)
	}
	if unread.incCalls != 0 {
		t.Error("查不到成员就不该加未读")
	}
}

// 没有成员列表能力时不该加未读（也不能 panic）。
func TestMessage_SendMessageWithoutListerSkipsUnread(t *testing.T) {
	uc, repo, membership, _, unread := newTestMessageUsecase()
	membership.add(7, "uid-42")

	if _, err := uc.SendMessage(context.Background(), "uid-42", 7, MessageTypeText, "hi"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if unread.incCalls != 0 {
		t.Error("没有成员列表能力就不该加未读")
	}
	if repo.createCalls != 1 {
		t.Error("消息应该已落库")
	}
}

// 未读数下游不可达时返 503，不是 500。
//
// 500 会让调用方以为是自己请求写错了，实际是服务端的问题，调用方可以重试。
func TestMessage_UnreadDownstreamFailureIs503(t *testing.T) {
	uc, _, membership, _, unread := newTestMessageUsecase()
	membership.add(7, "uid-42")
	unread.err = errors.New("conversation service downstream unavailable")

	// 查单个会话
	if _, _, err := uc.GetUnreadCount(context.Background(), "uid-42", 7); err == nil {
		t.Fatal("下游失败应该返错")
	} else if !errors.Is(err, ErrMessageConversationUnavailable) {
		t.Errorf("err = %v, want ErrMessageConversationUnavailable", err)
	}

	// 查全部会话
	if _, _, err := uc.GetUnreadCount(context.Background(), "uid-42", 0); err == nil {
		t.Fatal("下游失败应该返错")
	} else if !errors.Is(err, ErrMessageConversationUnavailable) {
		t.Errorf("err = %v, want ErrMessageConversationUnavailable", err)
	}

	// 清零
	if _, err := uc.MarkRead(context.Background(), "uid-42", 7); err == nil {
		t.Fatal("下游失败应该返错")
	} else if !errors.Is(err, ErrMessageConversationUnavailable) {
		t.Errorf("err = %v, want ErrMessageConversationUnavailable", err)
	}
}

// 「成员不存在」是调用方的错，保留 404，不要一律 503。
func TestMessage_UnreadNotFoundKeepsOriginalError(t *testing.T) {
	uc, _, membership, _, unread := newTestMessageUsecase()
	membership.add(7, "uid-42")
	unread.err = kratoserrors.NotFound("CONVERSATION_NOT_FOUND", "conversation not found")

	_, _, err := uc.GetUnreadCount(context.Background(), "uid-42", 7)
	if err == nil {
		t.Fatal("应该返错")
	}
	if errors.Is(err, ErrMessageConversationUnavailable) {
		t.Error("成员不存在不该被映射成 503")
	}
	var se *kratoserrors.Error
	if !errors.As(err, &se) || se.Code != 404 {
		t.Errorf("err = %v, want 404", err)
	}
}

// 没有成员校验能力时要**拒绝**，不是放行。nil 只可能来自接线配错。放行等于「消息服务完全不做鉴权」——
// 谁都能往别人的会话发消息、读别人的聊天记录。
func TestMessage_NilMembershipFailsClosed(t *testing.T) {
	repo := newFakeMessageRepo()
	uc := NewMessageUsecase(repo, nil, &fakePublisher{}, newFakeUnreadCounter(), nil, nil)

	// 发消息要拒。
	if _, err := uc.SendMessage(context.Background(), "uid-42", 7, MessageTypeText, "hi"); err == nil {
		t.Fatal("没有成员校验能力时发消息应该被拒")
	} else if !errors.Is(err, ErrMessageConversationUnavailable) {
		t.Errorf("err = %v, want ErrMessageConversationUnavailable", err)
	}
	if repo.createCalls != 0 {
		t.Error("被拒的消息不该落库")
	}

	// 读历史也要拒。
	if _, _, err := uc.ListMessages(context.Background(), "uid-42", 7, 0, 20); err == nil {
		t.Fatal("没有成员校验能力时读历史应该被拒")
	}
}
