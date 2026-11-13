package gateway

import (
	"context"
	"sync"
	"time"
)

// MemberFetcher 取会话成员列表（算广播目标用）。
type MemberFetcher interface {
	// ListConversationMembers 返回会话成员 uid 列表，conversationID 是会话 ID。
	ListConversationMembers(ctx context.Context, conversationID uint64) ([]string, error)
}

// MemberFetcherFunc 把函数适配成 MemberFetcher。
type MemberFetcherFunc func(ctx context.Context, conversationID uint64) ([]string, error)

// ListConversationMembers 调 f，conversationID 是会话 ID。
func (f MemberFetcherFunc) ListConversationMembers(ctx context.Context, conversationID uint64) ([]string, error) {
	return f(ctx, conversationID)
}

// UserConversationLister 列某用户参与的所有会话 id。
// 跟 MemberFetcher 反方向：这个问「这个人在哪些会话」。
type UserConversationLister interface {
	// ListUserConversations 返回该用户参与的会话 id 列表，userID 是用户 ID。
	ListUserConversations(ctx context.Context, userID string) ([]uint64, error)
}

// UserConversationListerFunc 把函数适配成 UserConversationLister。
type UserConversationListerFunc func(ctx context.Context, userID string) ([]uint64, error)

// ListUserConversations 调 f，userID 是用户 ID。
func (f UserConversationListerFunc) ListUserConversations(ctx context.Context, userID string) ([]uint64, error) {
	return f(ctx, userID)
}

// MemberCache 会话成员缓存。广播时每条消息都去问 conversation 服务太贵，用 TTL 兜底缓存。
// 群成员变动后缓存最坏会不准一个 TTL。
type MemberCache struct {
	fetcher MemberFetcher
	ttl     time.Duration

	mu      sync.RWMutex
	entries map[uint64]memberEntry
}

// memberEntry 一条缓存。
type memberEntry struct {
	members   []string
	fetchedAt time.Time
}

// NewMemberCache 构造 MemberCache。
// fetcher 回源查询器，为 nil 时 Get 返 nil
// ttl 缓存有效期，<= 0 用默认值
func NewMemberCache(fetcher MemberFetcher, ttl time.Duration) *MemberCache {
	if ttl <= 0 {
		ttl = defaultMemberCacheTTL
	}
	return &MemberCache{
		fetcher: fetcher,
		ttl:     ttl,
		entries: make(map[uint64]memberEntry),
	}
}

// defaultMemberCacheTTL 成员缓存有效期。
const defaultMemberCacheTTL = time.Minute

// Get 取会话成员，conversationID 是会话 ID。命中且未过期直接返回；否则回源并缓存。
func (c *MemberCache) Get(ctx context.Context, conversationID uint64) ([]string, error) {
	if c == nil || c.fetcher == nil {
		return nil, nil
	}

	c.mu.RLock()
	entry, ok := c.entries[conversationID]
	c.mu.RUnlock()
	if ok && time.Since(entry.fetchedAt) < c.ttl {
		return entry.members, nil
	}

	members, err := c.fetcher.ListConversationMembers(ctx, conversationID)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.entries[conversationID] = memberEntry{members: members, fetchedAt: time.Now()}
	c.mu.Unlock()
	return members, nil
}

// Invalidate 让某个会话的成员缓存失效，conversationID 是会话 ID。
func (c *MemberCache) Invalidate(conversationID uint64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.entries, conversationID)
	c.mu.Unlock()
}
