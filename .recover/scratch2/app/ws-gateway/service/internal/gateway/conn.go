// Package gateway 实现 WebSocket 网关。网关无状态：不落库，只维护连接 + 转发。
package gateway

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// Connection 一条 WebSocket 连接。
//
// 并发模型：单写者 —— 只有写协程能调 WriteMessage，其它协程通过 Send 往队列塞。
// gorilla/websocket 不允许并发写同一连接，并发写会 panic。
type Connection struct {
	UserID string
	Token  string // 握手时带的 JWT，转发消息到 message 服务时要用
	conn   *websocket.Conn

	// send 是写队列。满了丢弃最老的一条再塞新的。
	send chan []byte

	// closeOnce 保证 Close 只关一次（读协程和写协程都会调）。
	closeOnce sync.Once
	// closed 在 Close 时被关掉。所有需要感知「连接已死」的地方 select 它。
	closed chan struct{}

	// lastActive 最近一次收到数据的时刻（心跳续期用）。
	// 只有读协程写，用 atomic 而不是锁 —— 空闲驱逐任务会高频读它。
	lastActive atomic.Int64
}

// NewConnection 构造 Connection。sendQueueSize 是写队列长度。
func NewConnection(ws *websocket.Conn, userID string, token string, sendQueueSize int) *Connection {
	if sendQueueSize <= 0 {
		sendQueueSize = defaultSendQueueSize
	}
	c := &Connection{
		UserID: userID,
		Token:  token,
		conn:   ws,
		send:   make(chan []byte, sendQueueSize),
		closed: make(chan struct{}),
	}
	c.lastActive.Store(time.Now().UnixNano())
	return c
}

// Send 往写队列里塞一条消息。队列满时丢弃最老的一条再塞新的。
// 返回 false 表示连接已关闭。
//
// 为什么满了要丢最老的而不是丢新的：队列满说明客户端消费不过来（网络慢 / 卡住）。
// 这种情况下留着最新的更有价值 —— 聊天场景里丢一条历史消息比丢一条刚发的消息伤害小。
func (c *Connection) Send(payload []byte) bool {
	// 先看连接是否已关。已经关了就没必要再塞。
	select {
	case <-c.closed:
		return false
	default:
	}

	// 快路径：队列没满。
	select {
	case c.send <- payload:
		return true
	default:
	}

	// 队列满了 —— 腾出一个位置，丢最老的那条。
	select {
	case <-c.send:
	default:
	}

	// 再塞一次。这次要等位置空出来。
	select {
	case c.send <- payload:
		return true
	case <-c.closed:
		return false
	}
}

// Close 关闭连接。幂等 —— 读协程和写协程都会调它。
func (c *Connection) Close() {
	c.closeOnce.Do(func() {
		close(c.closed)
		// conn 可能为 nil（测试里造的连接没有底层 socket）
		if c.conn != nil {
			_ = c.conn.Close()
		}
	})
}

// IsClosed 连接是否已关闭。
func (c *Connection) IsClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

// Touch 记录最近活跃时间（心跳续期）。
func (c *Connection) Touch() {
	c.lastActive.Store(time.Now().UnixNano())
}

// LastActive 最近活跃时间。
func (c *Connection) LastActive() time.Time {
	return time.Unix(0, c.lastActive.Load())
}

// IdleFor 空闲了多久。
func (c *Connection) IdleFor() time.Duration {
	return time.Since(c.LastActive())
}
