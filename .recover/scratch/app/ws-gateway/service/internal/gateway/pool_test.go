package gateway

import (
	"testing"
	"time"
)

// newTestConn 造一条不依赖真实 websocket 的连接（conn 为 nil，Close 会
// 忽略 nil 错误 —— NewConnection 里 conn.Close() 的返回值被丢弃）。
func newTestConn(userID string) *Connection {
	return NewConnection(nil, userID, "token-"+userID, 4)
}
func TestPool_AddAndGet(t *testing.T) {
	p := NewPool(nil)
	c := newTestConn("uid-42")

	p.Add(c)
	if p.Count("uid-42") != 1 {
		t.Errorf("count = %d, want 1", p.Count("uid-42"))
	}
	if p.Total() != 1 {
		t.Errorf("total = %d, want 1", p.Total())
	}
	if p.OnlineCount() != 1 {
		t.Errorf("online count = %d, want 1", p.OnlineCount())
	}
	got := p.Get("uid-42")
	if len(got) != 1 || got[0] != c {
		t.Errorf("get = %v, want [%v]", got, c)
	}
}

// TestPool_MultipleConnectionsSameUser 同一用户多端登录 —— 连接都在池子里。
func TestPool_MultipleConnectionsSameUser(t *testing.T) {
	p := NewPool(nil)
	c1 := newTestConn("uid-42")
	c2 := newTestConn("uid-42")

	p.Add(c1)
	p.Add(c2)

	if p.Count("uid-42") != 2 {
		t.Errorf("count = %d, want 2", p.Count("uid-42"))
	}
	// 在线用户数是 1（一个人），连接数是 2。
	if p.OnlineCount() != 1 {
		t.Errorf("online count = %d, want 1", p.OnlineCount())
	}
	if p.Total() != 2 {
		t.Errorf("total = %d, want 2", p.Total())
	}
}

// TestPool_RemoveOnlyOneConnection 移除一条不能影响该用户的其它连接 ——
// 关掉手机端不能让网页端掉线。
func TestPool_RemoveOnlyOneConnection(t *testing.T) {
	p := NewPool(nil)
	c1 := newTestConn("uid-42")
	c2 := newTestConn("uid-42")
	p.Add(c1)
	p.Add(c2)

	p.Remove(c1)
	if p.Count("uid-42") != 1 {
		t.Errorf("count = %d, want 1", p.Count("uid-42"))
	}
	got := p.Get("uid-42")
	if len(got) != 1 || got[0] != c2 {
		t.Errorf("剩下的连接不对: %v", got)
	}
}

// TestPool_RemoveLastConnectionDropsUser 移除最后一条连接后用户不再在线。
func TestPool_RemoveLastConnectionDropsUser(t *testing.T) {
	p := NewPool(nil)
	c := newTestConn("uid-42")
	p.Add(c)

	p.Remove(c)
	if p.Count("uid-42") != 0 {
		t.Errorf("count = %d, want 0", p.Count("uid-42"))
	}
	if p.OnlineCount() != 0 {
		t.Errorf("online count = %d, want 0", p.OnlineCount())
	}
	if p.Get("uid-42") != nil {
		t.Error("没连接时 Get 该返 nil")
	}
}

// TestPool_RemoveNotInPool 移除一条不在池子里的连接不该报错，也别触发回调。
func TestPool_RemoveNotInPool(t *testing.T) {
	var closed int
	p := NewPool(func(string, *Connection) { closed++ })

	p.Remove(newTestConn("uid-42"))
	if closed != 0 {
		t.Errorf("回调次数 = %d, want 0", closed)
	}
}

// TestPool_OnCloseCalledOnRemove 移除连接要触发回调（清在线状态用）。
func TestPool_OnCloseCalledOnRemove(t *testing.T) {
	var gotUser string
	called := 0
	p := NewPool(func(userID string, _ *Connection) {
		gotUser = userID
		called++
	})

	c := newTestConn("uid-42")
	p.Add(c)
	p.Remove(c)

	if called != 1 {
		t.Errorf("回调次数 = %d, want 1", called)
	}
	if gotUser != "uid-42" {
		t.Errorf("回调 user id = %q, want uid-42", gotUser)
	}
}

// TestPool_OnCloseCanQueryPool 回调里调 pool.Count 不能死锁 —— 移除时
// 锁必须已经放掉。
func TestPool_OnCloseCanQueryPool(t *testing.T) {
	var count int
	var p *Pool
	p = NewPool(func(userID string, _ *Connection) {
		count = p.Count(userID)
	})

	c := newTestConn("uid-42")
	p.Add(c)
	p.Remove(c)

	if count != 0 {
		t.Errorf("回调里看到的连接数 = %d, want 0", count)
	}
}

// TestPool_AddNilIgnored nil 连接不该进池子。
func TestPool_AddNilIgnored(t *testing.T) {
	p := NewPool(nil)
	p.Add(nil)
	if p.Total() != 0 {
		t.Errorf("total = %d, want 0", p.Total())
	}
}

// TestPool_GetReturnsCopy Get 返回副本 —— 调用方遍历时不受并发写影响。
func TestPool_GetReturnsCopy(t *testing.T) {
	p := NewPool(nil)
	c := newTestConn("uid-42")
	p.Add(c)

	got := p.Get("uid-42")
	got[0] = newTestConn("uid-99")

	if p.Get("uid-42")[0] != c {
		t.Error("改了副本不该影响池子")
	}
}

// TestPool_Users 列出所有在线用户。
func TestPool_Users(t *testing.T) {
	p := NewPool(nil)
	p.Add(newTestConn("uid-42"))
	p.Add(newTestConn("uid-99"))

	users := p.Users()
	if len(users) != 2 {
		t.Fatalf("users = %v, want 2 个", users)
	}
	seen := map[string]bool{users[0]: true, users[1]: true}
	if !seen["uid-42"] || !seen["uid-99"] {
		t.Errorf("users = %v, want 含 42 和 99", users)
	}
}

// TestPool_All 全部连接。
func TestPool_All(t *testing.T) {
	p := NewPool(nil)
	p.Add(newTestConn("uid-42"))
	p.Add(newTestConn("uid-42"))
	p.Add(newTestConn("uid-99"))

	if len(p.All()) != 3 {
		t.Errorf("all = %d 条, want 3", len(p.All()))
	}
}

// TestPool_CloseAll 关掉所有连接。
func TestPool_CloseAll(t *testing.T) {
	p := NewPool(nil)
	c1 := newTestConn("uid-42")
	c2 := newTestConn("uid-99")
	p.Add(c1)
	p.Add(c2)

	p.CloseAll()

	if p.Total() != 0 {
		t.Errorf("total = %d, want 0", p.Total())
	}
	if !c1.IsClosed() || !c2.IsClosed() {
		t.Error("连接该被关掉")
	}
}

// TestPool_EvictIdle 空闲超时的连接被踢掉，活跃的留下。
func TestPool_EvictIdle(t *testing.T) {
	p := NewPool(nil)
	idle := newTestConn("uid-42")
	active := newTestConn("uid-99")
	p.Add(idle)
	p.Add(active)

	// 让 idle 那条看起来很久没活动了。
	idle.lastActive.Store(time.Now().Add(-10 * time.Minute).UnixNano())

	evicted := p.EvictIdle(time.Minute)
	if evicted != 1 {
		t.Errorf("踢掉数 = %d, want 1", evicted)
	}
	if p.Count("uid-42") != 0 {
		t.Errorf("空闲连接没被踢: count = %d", p.Count("uid-42"))
	}
	if p.Count("uid-99") != 1 {
		t.Errorf("活跃连接被误踢: count = %d", p.Count("uid-99"))
	}
}

// TestPool_EvictIdleDisabled 超时 <= 0 时不踢任何连接。
func TestPool_EvictIdleDisabled(t *testing.T) {
	p := NewPool(nil)
	c := newTestConn("uid-42")
	p.Add(c)
	c.lastActive.Store(time.Now().Add(-time.Hour).UnixNano())

	if n := p.EvictIdle(0); n != 0 {
		t.Errorf("踢掉数 = %d, want 0", n)
	}
	if p.Count("uid-42") != 1 {
		t.Error("不该踢连接")
	}
}
func TestConnection_SendQueuesPayload(t *testing.T) {
	c := newTestConn("uid-42")
	if !c.Send([]byte("a")) {
		t.Fatal("Send 该返 true")
	}
	if len(c.send) != 1 {
		t.Errorf("队列长度 = %d, want 1", len(c.send))
	}
}

// TestConnection_SendDropsOldestWhenFull 队列满时丢最老的一条 —— 保留最新的。
func TestConnection_SendDropsOldestWhenFull(t *testing.T) {
	c := NewConnection(nil, "uid-42", "t", 2)
	c.Send([]byte("old1"))
	c.Send([]byte("old2"))
	c.Send([]byte("new"))

	if len(c.send) != 2 {
		t.Fatalf("队列长度 = %d, want 2", len(c.send))
	}
	// 队首是最老的那条 —— 它被挤掉了；最新的一条留在队尾。
	if got := string(<-c.send); got != "old2" {
		t.Errorf("队首 = %q, want %q", got, "old2")
	}
	if got := string(<-c.send); got != "new" {
		t.Errorf("队尾 = %q, want %q", got, "new")
	}
}

// TestConnection_SendAfterCloseReturnsFalse 连接关了就不能再塞。
func TestConnection_SendAfterCloseReturnsFalse(t *testing.T) {
	c := newTestConn("uid-42")
	c.Close()
	if c.Send([]byte("a")) {
		t.Error("关闭后 Send 该返 false")
	}
}

// TestConnection_CloseIsIdempotent 关多次不能 panic。
func TestConnection_CloseIsIdempotent(t *testing.T) {
	c := newTestConn("uid-42")
	c.Close()
	c.Close()
	if !c.IsClosed() {
		t.Error("该是已关闭状态")
	}
}

// TestConnection_TouchResetsIdle 活跃时间被刷新。
func TestConnection_TouchResetsIdle(t *testing.T) {
	c := newTestConn("uid-42")
	c.lastActive.Store(time.Now().Add(-time.Hour).UnixNano())
	if c.IdleFor() < 30*time.Minute {
		t.Fatalf("idle = %v, want ~1h", c.IdleFor())
	}
	c.Touch()
	if c.IdleFor() > time.Second {
		t.Errorf("idle = %v, want ~0", c.IdleFor())
	}
}
