package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeTokenVerifier 是 TokenVerifier 的替身。
type fakeTokenVerifier struct {
	// uid 校验通过时返回的用户 id。
	uid string
	// err 校验失败时返回的错误。
	err error
	// seen 记录校验过的 token。
	seen []string
}

func (f *fakeTokenVerifier) Verify(raw string) (string, error) {
	f.seen = append(f.seen, raw)
	if f.err != nil {
		return "", f.err
	}
	return f.uid, nil
}

// fakeMessageSender 是 MessageSender 的替身。
type fakeMessageSender struct {
	// msgID 返回的消息 id。
	msgID uint64
	// createdAt 返回的创建时间。
	createdAt int64
	// err 转发失败时返回的错误。
	err error
	// calls 调用次数。
	calls int
	// gotToken 收到的 token。
	gotToken string
	// gotSender 收到的发送方 uid。
	gotSender string
	// gotConversation 收到的会话 id。
	gotConversation uint64
	// gotMsgType 收到的消息类型。
	gotMsgType uint8
	// gotContent 收到的内容。
	gotContent string
}

func (f *fakeMessageSender) Send(_ context.Context, token string, senderID string, conversationID uint64, msgType uint8, content string) (uint64, int64, error) {
	f.calls++
	f.gotToken = token
	f.gotSender = senderID
	f.gotConversation = conversationID
	f.gotMsgType = msgType
	f.gotContent = content
	if f.err != nil {
		return 0, 0, f.err
	}
	return f.msgID, f.createdAt, nil
}

// newTestGateway 构造一个网关 + 一个真实的 WS 测试服务器。t 是测试用例。
// 返回：网关、连接池、发消息替身、token 校验替身、服务器 URL。
func newTestGateway(t *testing.T) (*Server, *Pool, *fakeMessageSender, *fakeTokenVerifier, string) {
	t.Helper()

	pool := NewPool(nil)
	sender := &fakeMessageSender{msgID: 55, createdAt: 1700000000}
	verifier := &fakeTokenVerifier{uid: "uid-42"}

	s := NewServer(Config{
		Path:          "/ws",
		InstanceID:    "test-instance",
		SendQueueSize: 16,
		// 心跳参数调小，测试里能快速触发。
		PingInterval: 50 * time.Millisecond,
		PongWait:     200 * time.Millisecond,
		WriteWait:    time.Second,
	}, pool, sender, verifier, nil, nil, nil)

	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	return s, pool, sender, verifier, "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
}

// dialWS 发起一次 WS 握手。token 非空时带上 query 参数。
func dialWS(t *testing.T, url, token string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	if token == "" {
		return dialer.Dial(url, nil)
	}
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	return dialer.Dial(url+sep+"token="+token, nil)
}

// readFrame 读一帧并解析成 ServerFrame。
func readFrame(t *testing.T, conn *websocket.Conn) (*ServerFrame, error) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, payload, err := conn.ReadMessage()
	if err != nil {
		return nil, err
	}
	var f ServerFrame
	if err := json.Unmarshal(payload, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// TestServer_HandshakeRejectsMissingToken 没 token 握手要 401 —— 不能
// 让匿名连接进来。
func TestServer_HandshakeRejectsMissingToken(t *testing.T) {
	_, _, _, _, url := newTestGateway(t)

	conn, resp, err := dialWS(t, url, "")
	if err == nil {
		conn.Close()
		t.Fatal("没 token 不该握手成功")
	}
	if resp == nil {
		t.Fatal("该拿到 HTTP 响应")
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// TestServer_HandshakeRejectsInvalidToken token 非法要 401。
func TestServer_HandshakeRejectsInvalidToken(t *testing.T) {
	_, _, _, verifier, url := newTestGateway(t)
	verifier.err = errors.New("bad token")

	conn, resp, err := dialWS(t, url, "bad")
	if err == nil {
		conn.Close()
		t.Fatal("非法 token 不该握手成功")
	}
	if resp == nil {
		t.Fatal("该拿到 HTTP 响应")
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// TestServer_HandshakeRegistersConnection 握手成功后连接要进池子。
func TestServer_HandshakeRegistersConnection(t *testing.T) {
	_, pool, _, _, url := newTestGateway(t)

	conn, _, err := dialWS(t, url, "good-token")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// 等握手处理完。
	deadline := time.Now().Add(2 * time.Second)
	for pool.Count("uid-42") == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if pool.Count("uid-42") != 1 {
		t.Errorf("连接数 = %d, want 1", pool.Count("uid-42"))
	}
}

// TestServer_ConnectionCarriesToken 连接要带上握手 token —— 网关转发时
// 靠它识别用户。
func TestServer_ConnectionCarriesToken(t *testing.T) {
	_, pool, _, _, url := newTestGateway(t)

	conn, _, err := dialWS(t, url, "my-token")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	deadline := time.Now().Add(2 * time.Second)
	var got *Connection
	for time.Now().Before(deadline) {
		list := pool.Get("uid-42")
		if len(list) > 0 {
			got = list[0]
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got == nil {
		t.Fatal("连接没进池子")
	}
	if got.Token != "my-token" {
		t.Errorf("token = %q, want %q", got.Token, "my-token")
	}
}

// TestServer_PingGetsPong ping 帧要回 pong。
func TestServer_PingGetsPong(t *testing.T) {
	_, _, _, _, url := newTestGateway(t)

	conn, _, err := dialWS(t, url, "t")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"ping"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}

	frame, err := readFrame(t, conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if frame.Type != FramePong {
		t.Errorf("type = %v, want pong", frame.Type)
	}
}

// TestServer_ChatForwardsToMessageService chat 帧要转发到 message 服务，
// 并带上握手 token。
func TestServer_ChatForwardsToMessageService(t *testing.T) {
	_, _, sender, _, url := newTestGateway(t)

	conn, _, err := dialWS(t, url, "my-token")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	payload, _ := json.Marshal(&ClientFrame{
		Type:           FrameChat,
		ConversationID: 7,
		MsgType:        1,
		Content:        "hello",
	})
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("write: %v", err)
	}

	frame, err := readFrame(t, conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if frame.Type != FrameAck {
		t.Fatalf("type = %v, want ack", frame.Type)
	}

	if sender.calls != 1 {
		t.Errorf("转发次数 = %d, want 1", sender.calls)
	}
	if sender.gotToken != "my-token" {
		t.Errorf("token = %q, want %q", sender.gotToken, "my-token")
	}
	if sender.gotSender != "uid-42" {
		t.Errorf("sender id = %q, want \"uid-42\"", sender.gotSender)
	}
	if sender.gotConversation != 7 {
		t.Errorf("conversation id = %d, want 7", sender.gotConversation)
	}
	if sender.gotMsgType != 1 {
		t.Errorf("msg type = %d, want 1", sender.gotMsgType)
	}
	if sender.gotContent != "hello" {
		t.Errorf("content = %q, want %q", sender.gotContent, "hello")
	}
}

// TestServer_ChatAckCarriesMessageID ack 里要带落库后的消息 id。
func TestServer_ChatAckCarriesMessageID(t *testing.T) {
	_, _, _, _, url := newTestGateway(t)

	conn, _, err := dialWS(t, url, "t")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	payload, _ := json.Marshal(&ClientFrame{Type: FrameChat, ConversationID: 7, MsgType: 1, Content: "hi"})
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("write: %v", err)
	}

	frame, err := readFrame(t, conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var f struct {
		Type FrameType `json:"type"`
		Data AckData   `json:"data"`
	}
	raw, _ := json.Marshal(frame)
	_ = json.Unmarshal(raw, &f)
	if f.Data.MessageID != 55 {
		t.Errorf("message id = %d, want 55", f.Data.MessageID)
	}
}

// TestServer_ChatRejectsMissingConversationID 缺 conversation_id 要回错误帧，
// 不能转到 message 服务。
func TestServer_ChatRejectsMissingConversationID(t *testing.T) {
	_, _, sender, _, url := newTestGateway(t)

	conn, _, err := dialWS(t, url, "t")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"chat","content":"hi"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}

	frame, err := readFrame(t, conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if frame.Type != FrameError {
		t.Errorf("type = %v, want error", frame.Type)
	}
	if sender.calls != 0 {
		t.Error("参数不全不该转发")
	}
}

// TestServer_ChatRejectsEmptyContent 空内容不该转发。
func TestServer_ChatRejectsEmptyContent(t *testing.T) {
	_, _, sender, _, url := newTestGateway(t)

	conn, _, err := dialWS(t, url, "t")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	payload, _ := json.Marshal(&ClientFrame{Type: FrameChat, ConversationID: 7})
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("write: %v", err)
	}

	frame, err := readFrame(t, conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if frame.Type != FrameError {
		t.Errorf("type = %v, want error", frame.Type)
	}
	if sender.calls != 0 {
		t.Error("空内容不该转发")
	}
}

// TestServer_ChatReportsSendFailure 转发失败要回错误帧。
func TestServer_ChatReportsSendFailure(t *testing.T) {
	_, _, sender, _, url := newTestGateway(t)
	sender.err = errors.New("message service down")

	conn, _, err := dialWS(t, url, "t")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	payload, _ := json.Marshal(&ClientFrame{Type: FrameChat, ConversationID: 7, MsgType: 1, Content: "hi"})
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("write: %v", err)
	}

	frame, err := readFrame(t, conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if frame.Type != FrameError {
		t.Errorf("type = %v, want error", frame.Type)
	}
}

// TestServer_MalformedFrameGetsError 坏 JSON 要回错误帧，不能把连接搞崩。
func TestServer_MalformedFrameGetsError(t *testing.T) {
	_, _, _, _, url := newTestGateway(t)

	conn, _, err := dialWS(t, url, "t")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`not json`)); err != nil {
		t.Fatalf("write: %v", err)
	}

	frame, err := readFrame(t, conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if frame.Type != FrameError {
		t.Errorf("type = %v, want error", frame.Type)
	}
}

// TestServer_UnsupportedFrameTypeGetsError 未知帧类型要回错误帧。
func TestServer_UnsupportedFrameTypeGetsError(t *testing.T) {
	_, _, _, _, url := newTestGateway(t)

	conn, _, err := dialWS(t, url, "t")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"whatever"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}

	frame, err := readFrame(t, conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if frame.Type != FrameError {
		t.Errorf("type = %v, want error", frame.Type)
	}
}

// TestServer_BroadcastToMembers 广播要推给会话成员（发送方除外）。
func TestServer_BroadcastToMembers(t *testing.T) {
	s, pool, _, _, _ := newTestGateway(t)

	senderConn := newTestConn("uid-42")
	otherConn := newTestConn("uid-99")
	strangerConn := newTestConn("uid-77")
	pool.Add(senderConn)
	pool.Add(otherConn)
	pool.Add(strangerConn)

	payload, _ := json.Marshal(&PublishPayload{
		MessageID:      55,
		ConversationID: 7,
		SenderID:       "uid-42",
		Type:           1,
		Content:        "hello",
		CreatedAt:      1700000000,
	})
	s.BroadcastToMembers([]string{"uid-42", "uid-99"}, "uid-42", payload)

	// 收消息的那端要收到一帧 message。
	frame := readQueuedFrame(t, otherConn)
	if frame.Type != FrameMessage {
		t.Errorf("type = %v, want message", frame.Type)
	}
	// 发送方不该收到（它已经拿到 ack 了）。
	if len(senderConn.send) != 0 {
		t.Error("发送方不该收到广播")
	}
	// 非成员不该收到。
	if len(strangerConn.send) != 0 {
		t.Error("非成员不该收到广播")
	}
}

// TestServer_BroadcastReachesAllDevicesOfMember 同一成员的多条连接都要收到。
func TestServer_BroadcastReachesAllDevicesOfMember(t *testing.T) {
	s, pool, _, _, _ := newTestGateway(t)

	phone := newTestConn("uid-99")
	web := newTestConn("uid-99")
	pool.Add(phone)
	pool.Add(web)

	payload, _ := json.Marshal(&PublishPayload{
		MessageID: 55, ConversationID: 7, SenderID: "uid-42", Type: 1, Content: "hi",
	})
	s.BroadcastToMembers([]string{"uid-99"}, "uid-42", payload)

	readQueuedFrame(t, phone)
	readQueuedFrame(t, web)
}

// TestServer_HandleBroadcastUsesMemberCache 广播要按会话成员列表推。
func TestServer_HandleBroadcastUsesMemberCache(t *testing.T) {
	pool := NewPool(nil)
	sender := &fakeMessageSender{}
	verifier := &fakeTokenVerifier{uid: "uid-42"}

	cache := NewMemberCache(MemberFetcherFunc(func(_ context.Context, id uint64) ([]string, error) {
		return []string{"uid-42", "uid-99"}, nil
	}), time.Minute)

	s := NewServer(Config{Path: "/ws"}, pool, sender, verifier, cache, nil, nil)

	otherConn := newTestConn("uid-99")
	pool.Add(otherConn)

	payload, _ := json.Marshal(&PublishPayload{
		MessageID: 55, ConversationID: 7, SenderID: "uid-42", Type: 1, Content: "hi",
	})
	s.HandleBroadcast(payload)

	frame := readQueuedFrame(t, otherConn)
	if frame.Type != FrameMessage {
		t.Errorf("type = %v, want message", frame.Type)
	}
}

// TestServer_HandleBroadcastMalformedPayload 坏广播体要丢掉，不能 panic。
func TestServer_HandleBroadcastMalformedPayload(t *testing.T) {
	s, pool, _, _, _ := newTestGateway(t)
	c := newTestConn("uid-99")
	pool.Add(c)

	s.HandleBroadcast([]byte(`{`))

	if len(c.send) != 0 {
		t.Error("坏广播体不该推任何帧")
	}
}

// TestServer_HandleBroadcastWithoutMemberCache 没有成员查询能力时丢弃广播。
func TestServer_HandleBroadcastWithoutMemberCache(t *testing.T) {
	s, pool, _, _, _ := newTestGateway(t)
	c := newTestConn("uid-99")
	pool.Add(c)

	payload, _ := json.Marshal(&PublishPayload{
		MessageID: 55, ConversationID: 7, SenderID: "uid-42", Type: 1, Content: "hi",
	})
	s.HandleBroadcast(payload)

	if len(c.send) != 0 {
		t.Error("查不到成员不该推帧")
	}
}

// readQueuedFrame 从连接的写队列里读一帧并解析。
func readQueuedFrame(t *testing.T, c *Connection) *ServerFrame {
	t.Helper()
	select {
	case payload := <-c.send:
		var f ServerFrame
		if err := json.Unmarshal(payload, &f); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return &f
	case <-time.After(2 * time.Second):
		t.Fatal("没收到帧")
		return nil
	}
}

// TestServer_ServerPingsClient 服务端要发控制层 ping。gorilla 的控制帧不交给 ReadMessage —— 读协程内部会先调 ping handler。
// 所以这里靠 ping handler 被触发来判定服务端发了 ping。
func TestServer_ServerPingsClient(t *testing.T) {
	_, _, _, _, url := newTestGateway(t)

	conn, _, err := dialWS(t, url, "t")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	pinged := make(chan struct{}, 1)
	conn.SetPingHandler(func(appData string) error {
		select {
		case pinged <- struct{}{}:
		default:
		}
		// 回 pong，模拟客户端行为。
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(time.Second))
	})

	// 读协程必须跑起来，控制帧才会被处理。
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	select {
	case <-pinged:
	case <-time.After(3 * time.Second):
		t.Fatal("没收到服务端的 ping")
	}
}

// TestServer_DisconnectRemovesConnection 连接断了要从池子里移除。
func TestServer_DisconnectRemovesConnection(t *testing.T) {
	_, pool, _, _, url := newTestGateway(t)

	conn, _, err := dialWS(t, url, "t")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for pool.Count("uid-42") == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if pool.Count("uid-42") != 1 {
		t.Fatalf("连接数 = %d, want 1", pool.Count("uid-42"))
	}

	conn.Close()

	deadline = time.Now().Add(3 * time.Second)
	for pool.Count("uid-42") > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if pool.Count("uid-42") != 0 {
		t.Errorf("断开后连接数 = %d, want 0", pool.Count("uid-42"))
	}
}

// TestServer_Path 网关路径。
func TestServer_Path(t *testing.T) {
	s := NewServer(Config{}, NewPool(nil), nil, nil, nil, nil, nil)
	if s.Path() != "/ws" {
		t.Errorf("path = %q, want %q", s.Path(), "/ws")
	}
}

// 下游错误不能原样透给客户端 —— gRPC 的错误字符串里带着内部细节。
func TestClientFacingSendError_HidesInternalDetails(t *testing.T) {
	internal := errors.New("rpc error: code = Unavailable desc = connection error: desc = \"transport: Error while dialing dial tcp 127.0.0.1:9003\"")
	got := clientFacingSendError(internal)
	if strings.Contains(got, "127.0.0.1") || strings.Contains(got, "9003") || strings.Contains(got, "dial") {
		t.Errorf("错误文案泄露了内部细节: %q", got)
	}
	if got != "send failed" {
		t.Errorf("got = %q, want %q", got, "send failed")
	}
}

// 按下游 gRPC 状态码给出对应的人话。跨进程后拿到的是 gRPC 的 status 错误（kratos v3 的 grpc client 不还原
// *errors.Error），所以判的是 gRPC 码。
func TestClientFacingSendError_MapsCodes(t *testing.T) {
	cases := []struct {
		code codes.Code
		want string
	}{
		{codes.PermissionDenied, "you are not a member of this conversation"},
		{codes.InvalidArgument, "invalid message"},
		{codes.NotFound, "conversation not found"},
		{codes.Unavailable, "message service unavailable"},
	}
	for _, c := range cases {
		err := status.Error(c.code, "internal detail here")
		got := clientFacingSendError(err)
		if got != c.want {
			t.Errorf("code %v: got = %q, want %q", c.code, got, c.want)
		}
		if strings.Contains(got, "internal detail") {
			t.Errorf("code %v: 文案泄露了内部细节: %q", c.code, got)
		}
	}
}

// nil 错误不该 panic。
func TestClientFacingSendError_NilError(t *testing.T) {
	if got := clientFacingSendError(nil); got == "" {
		t.Error("nil 错误也该给一句文案")
	}
}
