package gateway

import (
	"testing"
	"time"
)

// newTestConn 造一条不依赖真实 websocket 的连接（conn 为 nil，Close 会忽略 nil 错误）。
func newTestConn(userID string) *Connection {
	return NewConnection(nil, userID, "token-"+userID, 4)
}

func TestConnection_SendQueues(t *testing.T) {
	c := newTestConn("uid-1")

	if !c.Send([]byte("a")) {
		t.Fatal("Send 该返回 true")
	}
	if !c.Send([]byte("b")) {
		t.Fatal("Send 该返回 true")
	}
	if len(c.send) != 2 {
		t.Errorf("队列长度 = %d, want 2", len(c.send))
	}
}

// TestConnection_SendDropsOldestWhenFull 队列满时丢最老的一条 ——
// 客户端消费不过来时，留着最新的更有价值。
func TestConnection_SendDropsOldestWhenFull(t *testing.T) {
	c := NewConnection(nil, "uid-1", "token", 2)

	c.Send([]byte("old1"))
	c.Send([]byte("old2"))
	c.Send([]byte("new"))

	if len(c.send) != 2 {
		t.Fatalf("队列长度 = %d, want 2", len(c.send))
	}
	// 最老的 "old1" 该被丢掉了。
	if got := string(<-c.send); got != "old2" {
		t.Errorf("队首 = %q, want old2", got)
	}
	if got := string(<-c.send); got != "new" {
		t.Errorf("队尾 = %q, want new", got)
	}
}

func TestConnection_SendAfterCloseReturnsFalse(t *testing.T) {
	c := newTestConn("uid-1")
	c.Close()

	if c.Send([]byte("x")) {
		t.Error("已关闭的连接 Send 该返 false")
	}
}

// TestConnection_CloseIsIdempotent 读协程和写协程都会调 Close ——
// 重复关不能 panic（close of closed channel）。
func TestConnection_CloseIsIdempotent(t *testing.T) {
	c := newTestConn("uid-1")
	c.Close()
	c.Close()

	if !c.IsClosed() {
		t.Error("该是已关闭状态")
	}
}

func TestConnection_TouchUpdatesLastActive(t *testing.T) {
	c := newTestConn("uid-1")

	// 造一个 100ms 前的活跃时间。
	c.lastActive.Store(time.Now().Add(-100 * time.Millisecond).UnixNano())
	before := c.IdleFor()
	if before < 90*time.Millisecond {
		t.Errorf("idle = %v, 该 >= 90ms", before)
	}

	c.Touch()
	if after := c.IdleFor(); after > 50*time.Millisecond {
		t.Errorf("Touch 后 idle = %v, 该很小", after)
	}
}

// TestConnection_SendAfterCloseDoesNotBlock 关掉连接后 Send 不能卡住 ——
// 它可能从广播协程里被调到，不能把广播协程拖死。
func TestConnection_SendAfterCloseDoesNotBlock(t *testing.T) {
	c := newTestConn("uid-1")
	c.Close()

	done := make(chan struct{})
	go func() {
		c.Send([]byte("x"))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Send 卡住了")
	}
}
