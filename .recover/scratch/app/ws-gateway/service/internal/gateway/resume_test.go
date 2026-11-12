package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeMessageLister 是 MessageLister 的替身。
type fakeMessageLister struct {
	// msgs 返回的消息。
	msgs []MessageInfo
	// last 返回的最后一条消息 id。
	last uint64
	// hasMore 返回的「还有更多」标记。
	hasMore bool
	// err 查询失败时返回的错误。
	err error
	// calls 调用次数。
	calls int
	// gotToken 收到的 token。
	gotToken string
	// gotConversation 收到的会话 id。
	gotConversation uint64
	// gotSinceID 收到的基准消息 id。
	gotSinceID uint64
	// gotLimit 收到的单页大小。
	gotLimit int32
}

func (f *fakeMessageLister) ListSince(_ context.Context, token string, conversationID, sinceID uint64, limit int32) ([]MessageInfo, uint64, bool, error) {
	f.calls++
	f.gotToken = token
	f.gotConversation = conversationID
	f.gotSinceID = sinceID
	f.gotLimit = limit
	if f.err != nil {
		return nil, 0, false, f.err
	}
	return f.msgs, f.last, f.hasMore, nil
}

// newTestServer 起一个挂网关 handler 的 HTTP 测试服务器，返回 WS URL。
func newTestServer(t *testing.T, s *Server) string {
	t.Helper()
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
}

// newResumeGateway 构造一个带消息查询能力的网关。
func newResumeGateway(t *testing.T, lister *fakeMessageLister) (*Server, string) {
	t.Helper()
	pool := NewPool(nil)
	verifier := &fakeTokenVerifier{uid: "uid-42"}
	s := NewServer(Config{
		Path:          "/ws",
		InstanceID:    "test-instance",
		SendQueueSize: 64,
		PingInterval:  50 * time.Millisecond,
		PongWait:      200 * time.Millisecond,
		WriteWait:     time.Second,
	}, pool, &fakeMessageSender{}, verifier, nil, nil, nil)
	s.WithMessageLister(lister)

	return s, newTestServer(t, s)
}

// TestServer_ResumeReplaysMissedMessages 断线重连后要把断线期间漏掉
// 的消息补回来。
func TestServer_ResumeReplaysMissedMessages(t *testing.T) {
	lister := &fakeMessageLister{
		msgs: []MessageInfo{
			{ID: 101, ConversationID: 7, SenderID: "uid-42", Type: 1, Content: "missed-1", CreatedAt: 1700000001},
			{ID: 102, ConversationID: 7, SenderID: "uid-43", Type: 1, Content: "missed-2", CreatedAt: 1700000002},
		},
		last: 102,
	}
	_, url := newResumeGateway(t, lister)

	conn, _, err := dialWS(t, url, "token-abc")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// 发 resume 帧：会话 7，最后看到的是 100。
	if err := conn.WriteJSON(map[string]any{
		"type":            "resume",
		"conversation_id": 7,
		"last_message_id": 100,
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// 期望收到两条 message 帧 + 一个 resume_result 帧。
	var gotMessages []string
	var result *ResumeData
	for i := 0; i < 3; i++ {
		f, err := readFrame(t, conn)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		switch f.Type {
		case FrameMessage:
			var d MessageData
			raw, _ := json.Marshal(f.Data)
			_ = json.Unmarshal(raw, &d)
			gotMessages = append(gotMessages, d.Content)
		case FrameResumeResult:
			raw, _ := json.Marshal(f.Data)
			result = &ResumeData{}
			_ = json.Unmarshal(raw, result)
		default:
			t.Fatalf("意外的帧类型: %s", f.Type)
		}
	}

	if len(gotMessages) != 2 {
		t.Fatalf("补回消息数 = %d, want 2 (%v)", len(gotMessages), gotMessages)
	}
	// 顺序必须是旧的在前 —— 客户端按顺序塞进本地列表。
	if gotMessages[0] != "missed-1" || gotMessages[1] != "missed-2" {
		t.Errorf("补回消息顺序 = %v, want [missed-1 missed-2]", gotMessages)
	}
	if result == nil {
		t.Fatal("该收到 resume_result 帧")
	}
	if result.Count != 2 {
		t.Errorf("count = %d, want 2", result.Count)
	}
	if result.LastMessageID != 102 {
		t.Errorf("last_message_id = %d, want 102", result.LastMessageID)
	}
	if result.HasMore {
		t.Error("has_more = true, want false")
	}

	// 网关要拿连接上的 token 去问下游 —— message 服务靠它识别用户。
	if lister.gotToken != "token-abc" {
		t.Errorf("token = %q, want %q", lister.gotToken, "token-abc")
	}
	if lister.gotConversation != 7 {
		t.Errorf("conversation_id = %d, want 7", lister.gotConversation)
	}
	if lister.gotSinceID != 100 {
		t.Errorf("since_id = %d, want 100", lister.gotSinceID)
	}
}

// TestServer_ResumeReportsHasMore 补不完时要告诉客户端还有更多 ——
// 客户端才知道要继续拉。
func TestServer_ResumeReportsHasMore(t *testing.T) {
	lister := &fakeMessageLister{msgs: []MessageInfo{{ID: 1, ConversationID: 7}}, last: 1, hasMore: true}
	_, url := newResumeGateway(t, lister)

	conn, _, err := dialWS(t, url, "tok")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]any{
		"type":            "resume",
		"conversation_id": 7,
		"last_message_id": 0,
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// 第一帧是 message，第二帧是 resume_result。
	f1, err := readFrame(t, conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if f1.Type != FrameMessage {
		t.Errorf("第一帧类型 = %s, want %s", f1.Type, FrameMessage)
	}
	f2, err := readFrame(t, conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if f2.Type != FrameResumeResult {
		t.Fatalf("第二帧类型 = %s, want %s", f2.Type, FrameResumeResult)
	}
	raw, _ := json.Marshal(f2.Data)
	var d ResumeData
	_ = json.Unmarshal(raw, &d)
	if !d.HasMore {
		t.Error("has_more = false, want true")
	}
}

// TestServer_ResumeRequiresConversationID 没会话 id 要回错误帧。
func TestServer_ResumeRequiresConversationID(t *testing.T) {
	lister := &fakeMessageLister{}
	_, url := newResumeGateway(t, lister)

	conn, _, err := dialWS(t, url, "tok")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]any{"type": "resume"}); err != nil {
		t.Fatalf("write: %v", err)
	}

	f, err := readFrame(t, conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if f.Type != FrameError {
		t.Fatalf("帧类型 = %s, want %s", f.Type, FrameError)
	}
	if lister.calls != 0 {
		t.Errorf("下游被调了 %d 次, want 0", lister.calls)
	}
}

// TestServer_ResumeWithoutListerFails 没接消息查询能力时要明确报错 ——
// 不能静默返「补了 0 条」，客户端会以为没漏消息。
func TestServer_ResumeWithoutListerFails(t *testing.T) {
	pool := NewPool(nil)
	s := NewServer(Config{Path: "/ws", InstanceID: "x"}, pool, &fakeMessageSender{}, &fakeTokenVerifier{uid: "uid-42"}, nil, nil, nil)
	url := newTestServer(t, s)

	conn, _, err := dialWS(t, url, "tok")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]any{"type": "resume", "conversation_id": 7}); err != nil {
		t.Fatalf("write: %v", err)
	}

	f, err := readFrame(t, conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if f.Type != FrameError {
		t.Fatalf("帧类型 = %s, want %s", f.Type, FrameError)
	}
}

// TestServer_ResumeDownstreamError 下游挂了要回错误帧，不是静默。
func TestServer_ResumeDownstreamError(t *testing.T) {
	lister := &fakeMessageLister{err: errors.New("boom")}
	_, url := newResumeGateway(t, lister)

	conn, _, err := dialWS(t, url, "tok")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]any{"type": "resume", "conversation_id": 7}); err != nil {
		t.Fatalf("write: %v", err)
	}

	f, err := readFrame(t, conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if f.Type != FrameError {
		t.Fatalf("帧类型 = %s, want %s", f.Type, FrameError)
	}
}

// TestServer_ResumeUsesBoundedPageSize 补消息一页有上限 —— 一次补太多
// 会撑爆写队列，队列满了 Send 会丢最老的，等于丢消息。
func TestServer_ResumeUsesBoundedPageSize(t *testing.T) {
	lister := &fakeMessageLister{}
	_, url := newResumeGateway(t, lister)

	conn, _, err := dialWS(t, url, "tok")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]any{"type": "resume", "conversation_id": 7}); err != nil {
		t.Fatalf("write: %v", err)
	}

	f, err := readFrame(t, conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if f.Type != FrameResumeResult {
		t.Fatalf("帧类型 = %s, want %s", f.Type, FrameResumeResult)
	}
	if lister.gotLimit != resumePageSize {
		t.Errorf("limit = %d, want %d", lister.gotLimit, resumePageSize)
	}
	if resumePageSize > defaultSendQueueSize {
		t.Errorf("resumePageSize = %d, 超过默认写队列容量 %d", resumePageSize, defaultSendQueueSize)
	}
}

// TestServer_StatusRequiresArgument 既没 user_id 也没 conversation_id
// 时要回错误帧。
func TestServer_StatusRequiresArgument(t *testing.T) {
	_, _, _, _, url := newTestGateway(t)

	conn, _, err := dialWS(t, url, "tok")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]any{"type": "status"}); err != nil {
		t.Fatalf("write: %v", err)
	}

	f, err := readFrame(t, conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if f.Type != FrameError {
		t.Fatalf("帧类型 = %s, want %s", f.Type, FrameError)
	}
}

// TestServer_StatusReportsLocalUser 查本实例上的用户要答「在线」。
func TestServer_StatusReportsLocalUser(t *testing.T) {
	s, _, _, _, url := newTestGateway(t)

	conn, _, err := dialWS(t, url, "tok")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// 等握手处理完，连接进池子。
	deadline := time.Now().Add(2 * time.Second)
	for s.pool.Count("uid-42") == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if err := conn.WriteJSON(map[string]any{"type": "status", "user_id": "uid-42"}); err != nil {
		t.Fatalf("write: %v", err)
	}

	f, err := readFrame(t, conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if f.Type != FrameOnline {
		t.Fatalf("帧类型 = %s, want %s", f.Type, FrameOnline)
	}
	raw, _ := json.Marshal(f.Data)
	var d OnlineData
	_ = json.Unmarshal(raw, &d)
	if d.UserID != "uid-42" {
		t.Errorf("user_id = %q, want uid-42", d.UserID)
	}
	if !d.Online {
		t.Error("online = false, want true（本实例上有连接）")
	}
}

// TestServer_StatusReportsOfflineUser 没连着的用户要答「离线」。
func TestServer_StatusReportsOfflineUser(t *testing.T) {
	_, _, _, _, url := newTestGateway(t)

	conn, _, err := dialWS(t, url, "tok")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]any{"type": "status", "user_id": "uid-999"}); err != nil {
		t.Fatalf("write: %v", err)
	}

	f, err := readFrame(t, conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if f.Type != FrameOnline {
		t.Fatalf("帧类型 = %s, want %s", f.Type, FrameOnline)
	}
	raw, _ := json.Marshal(f.Data)
	var d OnlineData
	_ = json.Unmarshal(raw, &d)
	if d.Online {
		t.Error("online = true, want false")
	}
}

// TestNotifyOnlineStateChangeNotifiesPeers 用户上线时要通知会话里的同伴。
func TestNotifyOnlineStateChangeNotifiesPeers(t *testing.T) {
	pool := NewPool(nil)
	verifier := &fakeTokenVerifier{uid: "uid-42"}
	s := NewServer(Config{Path: "/ws", InstanceID: "x"}, pool, &fakeMessageSender{}, verifier, nil, nil, nil)
	// 会话 7 的成员是 42 和 43；本次变化的人是 42。
	s.WithUserConversations(UserConversationListerFunc(func(_ context.Context, userID string) ([]uint64, error) {
		if userID != "uid-42" {
			return nil, nil
		}
		return []uint64{7}, nil
	}))
	s.members = NewMemberCache(MemberFetcherFunc(func(_ context.Context, convID uint64) ([]string, error) {
		return []string{"uid-42", "uid-43"}, nil
	}), time.Minute)

	// 同伴 43 在本实例上有一���连接。
	peer := &Connection{UserID: "uid-43", send: make(chan []byte, 4), closed: make(chan struct{})}
	pool.Add(peer)

	n := s.NotifyOnlineStateChange("uid-42", true)
	if n != 1 {
		t.Errorf("通知人数 = %d, want 1", n)
	}
	select {
	case payload := <-peer.send:
		f := &ServerFrame{}
		_ = json.Unmarshal(payload, f)
		if f.Type != FrameOnline {
			t.Fatalf("帧类型 = %s, want %s", f.Type, FrameOnline)
		}
		raw, _ := json.Marshal(f.Data)
		var d OnlineData
		_ = json.Unmarshal(raw, &d)
		if d.UserID != "uid-42" || !d.Online {
			t.Errorf("online 帧 = %+v, want {uid-42 true}", d)
		}
	default:
		t.Fatal("同伴没收到 online 帧")
	}
}

// TestNotifyOnlineStateChangeSkipsSelf 不通知自己。
func TestNotifyOnlineStateChangeSkipsSelf(t *testing.T) {
	pool := NewPool(nil)
	s := NewServer(Config{Path: "/ws", InstanceID: "x"}, pool, &fakeMessageSender{}, &fakeTokenVerifier{uid: "uid-42"}, nil, nil, nil)
	s.WithUserConversations(UserConversationListerFunc(func(_ context.Context, _ string) ([]uint64, error) {
		return []uint64{7}, nil
	}))
	s.members = NewMemberCache(MemberFetcherFunc(func(_ context.Context, _ uint64) ([]string, error) {
		return []string{"uid-42"}, nil
	}), time.Minute)

	self := &Connection{UserID: "uid-42", send: make(chan []byte, 4), closed: make(chan struct{})}
	pool.Add(self)

	if n := s.NotifyOnlineStateChange("uid-42", false); n != 0 {
		t.Errorf("通知人数 = %d, want 0", n)
	}
	select {
	case <-self.send:
		t.Error("自己不该收到通知")
	default:
	}
}

// TestNotifyOnlineStateChangeWithoutLister 没接会话列表能力时不通知。
func TestNotifyOnlineStateChangeWithoutLister(t *testing.T) {
	pool := NewPool(nil)
	s := NewServer(Config{Path: "/ws", InstanceID: "x"}, pool, &fakeMessageSender{}, &fakeTokenVerifier{uid: "uid-42"}, nil, nil, nil)

	if n := s.NotifyOnlineStateChange("uid-42", true); n != 0 {
		t.Errorf("通知人数 = %d, want 0", n)
	}
}
