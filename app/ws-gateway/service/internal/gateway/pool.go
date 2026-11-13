package gateway

import (
	"slices"
	"sync"
	"time"
)

// Pool 连接池：uid → []*Connection。一个用户可以同时有多条连接（多端登录）。
type Pool struct {
	mu    sync.RWMutex
	conns map[string][]*Connection
	// 连接被移除时的回调（清理在线状态用）。可为 nil
	onClose func(userID string, c *Connection)
}

// NewPool 构造连接池。onClose 用于清理 Redis 里的在线状态，可为 nil。
func NewPool(onClose func(userID string, c *Connection)) *Pool {
	return &Pool{conns: make(map[string][]*Connection), onClose: onClose}
}

// Add 加一条连接。c 是要加入的连接。同一用户的多个连接都留着，消息推给所有端。
func (p *Pool) Add(c *Connection) {
	if c == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.conns[c.UserID] = append(p.conns[c.UserID], c)
}

// Remove 移除一条连接。c 是要移除的连接，按指针匹配只删这一条。
func (p *Pool) Remove(c *Connection) {
	if c == nil {
		return
	}
	p.mu.Lock()
	list := p.conns[c.UserID]
	kept := list[:0] // 复用底层数组，长度变为0，容量不变
	var removed bool
	for _, existing := range list {
		if existing == c {
			removed = true
			continue
		}
		kept = append(kept, existing)
	}
	// 没有连接了，删除key
	if len(kept) == 0 {
		delete(p.conns, c.UserID)
	} else {
		p.conns[c.UserID] = kept
	}
	p.mu.Unlock()

	if removed && p.onClose != nil {
		p.onClose(c.UserID, c)
	}
}

// Get 取某用户的全部连接副本，userID 是用户 ID。
func (p *Pool) Get(userID string) []*Connection {
	p.mu.RLock()
	defer p.mu.RUnlock()
	list := p.conns[userID]
	if len(list) == 0 {
		return nil
	}
	return slices.Clone(list)
}

// Users 当前有连接的用户 id 列表。
func (p *Pool) Users() []string {

	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, 0, len(p.conns))
	for id := range p.conns {
		out = append(out, id)
	}
	return out
}

// Count 某用户的连接数，userID 是用户 ID。
func (p *Pool) Count(userID string) int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.conns[userID])
}

// Total 总连接数。
func (p *Pool) Total() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	n := 0
	for _, list := range p.conns {
		n += len(list)
	}
	return n
}

// OnlineCount 有连接的用户数。
func (p *Pool) OnlineCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.conns)
}

// All 全部连接。
func (p *Pool) All() []*Connection {

	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*Connection, 0, p.Total())
	for _, list := range p.conns {
		out = append(out, list...)
	}
	return out
}

// CloseAll 关掉所有连接。
func (p *Pool) CloseAll() {
	for _, c := range p.All() {
		c.Close()
		p.Remove(c)
	}
}

// EvictIdle 踢掉空闲超时的连接，idleTimeout 是空闲阈值（<= 0 时不踢），返回踢掉的连接数。
func (p *Pool) EvictIdle(idleTimeout time.Duration) int {
	if idleTimeout <= 0 {
		return 0
	}
	var evicted int
	for _, c := range p.All() {
		if c.IdleFor() < idleTimeout {
			continue
		}
		c.Close()
		p.Remove(c)
		evicted++
	}
	return evicted
}
