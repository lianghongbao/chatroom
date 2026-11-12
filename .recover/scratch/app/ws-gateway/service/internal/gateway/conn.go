// Package gateway 实现 WebSocket 网关。网关无状态：不落库，只维护连接 + 转发。
package gateway

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// Connection 一条 WebSocket 连接。
// 并发模型：单写者 —— 只有写协程能调 WriteMessage，其它协程通过 Send 往队列塞。
type Connection struct {
	UserID string
	Token  string // 握手时带的 JWT，转发消息到 message 服务时要用
	conn   *websocket.Conn
	send   chan []byte // 写队列，满时丢弃最老的一条
	// closeOnce 保证 Close 只关一次（读协程和写协程都会调）。
	closeOnce sync.Once
	closed    chan struct{}
	// 最近一次收到数据的时刻（心跳续期用）。只由读协程写
	lastActive atomic.Int64
}

// NewConnection 构造 Connection，sendQueueSize 是写队列长度。
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
func (c *Connection) Send(payload []byte) bool {
	select {
	case <-c.closed:
		return false
	default:
	}
	select {
	case c.send <- payload:
		return true
	default:
		// 队列满 —— 腾出一个位置
		select {
		case <-c.send:
		default:
		}
		select {
		case c.send <- payload:
			return true
		case <-c.closed:
			return false
		}
	}
}

// Close 关闭连接。幂等。
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
