package gateway

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// slowMessageSender 模拟慢下游：Send 要花 1 秒。
type slowMessageSender struct {
	delay time.Duration
}

func (s *slowMessageSender) Send(_ context.Context, _ string, _ string, _ uint64, _ uint8, _ string) (uint64, int64, error) {
	time.Sleep(s.delay)
	return 55, 1700000000, nil
}

// TestReadPumpNotBlockedBySlowDownstream 下游慢的时候读协程不能被卡住 ——
// 否则这条连接收不到任何后续帧（包括 pong），会被心跳超时踢掉。
//
// 做法：先发一个 chat 帧（下游会阻塞 1 秒），紧接着发一个 ping 帧。
// 如果读协程被 chat 卡住，pong 就要等 1 秒以后才到。
func TestReadPumpNotBlockedBySlowDownstream(t *testing.T) {
	pool := NewPool(nil)
	s := NewServer(Config{
		Path:          "/ws",
		InstanceID:    "test-instance",
		SendQueueSize: 16,
		PingInterval:  50 * time.Millisecond,
		PongWait:      200 * time.Millisecond,
		WriteWait:     time.Second,
	}, pool, &slowMessageSender{delay: time.Second}, &fakeTokenVerifier{uid: "uid-42"}, nil, nil, nil)

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"

	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	conn, _, err := dialer.Dial(url+"?token=t", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// 先发 chat —— 下游会阻塞 1 秒。
	chatPayload, _ := json.Marshal(&ClientFrame{
		Type: FrameChat, ConversationID: 7, MsgType: 1, Content: "hi",
	})
	if err := conn.WriteMessage(websocket.TextMessage, chatPayload); err != nil {
		t.Fatalf("write chat: %v", err)
	}

	// 紧接着发 ping。如果读协程没被卡住，pong 很快就到。
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"ping"}`)); err != nil {
		t.Fatalf("write ping: %v", err)
	}

	// 等 pong。给了 800ms —— 比下游的 1 秒短。
	start := time.Now()
	_ = conn.SetReadDeadline(time.Now().Add(800 * time.Millisecond))
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("读帧失败（pong 没及时到）: %v", err)
		}
		var f ServerFrame
		if err := json.Unmarshal(payload, &f); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if f.Type == FramePong {
			elapsed := time.Since(start)
			t.Logf("pong 延迟 %v", elapsed)
			if elapsed > 800*time.Millisecond {
				t.Errorf("pong 延迟 %v —— 读协程被慢下游卡住了", elapsed)
			}
			return
		}
	}
}
