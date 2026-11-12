package data

import (
	"context"
	"testing"
	"time"

	"chatroom/conversation-service/internal/testutil"

	"github.com/redis/go-redis/v9"
)

// newTestConvCache 构造接真实 Redis 的 ConversationCache。
//
// 用 DB 15 —— 测试专用，跟应用的 DB 0 隔开。Redis 不可达时 skip。
//
// 返回 cache 和一个清理函数（删掉本次用到的 key）。
func newTestConvCache(t *testing.T) (*ConversationCache, redis.Cmdable) {
	t.Helper()

	rdb := redis.NewClient(&redis.Options{
		Addr:         testutil.RedisAddr(),
		DB:           15,
		DialTimeout:  2 * time.Second,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 2 * time.Second,
	})
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		t.Skipf("redis 不可达 (%s): %v", testutil.RedisAddr(), err)
	}
	t.Cleanup(func() { _ = rdb.Close() })

	return &ConversationCache{rdb: rdb, membersTTL: 5 * time.Minute}, rdb
}

// TestConversationCache_SetAndGetMembers 写入后能读回来，且是升序。
func TestConversationCache_SetAndGetMembers(t *testing.T) {
	c, rdb := newTestConvCache(t)
	ctx := context.Background()
	const convID = 900001

	t.Cleanup(func() { _ = rdb.Del(ctx, convMembersKey(convID)).Err() })

	// 故意乱序写入 —— 读出来必须是升序。
	c.SetMembers(ctx, convID, []uint64{30, 10, 20})

	got, err := c.Members(ctx, convID)
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("成员数 = %d, want 3", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Fatalf("成员应升序，实际 %v", got)
		}
	}
}

// TestConversationCache_MembersMissReturnsNil 未命中返 (nil, nil)。
func TestConversationCache_MembersMissReturnsNil(t *testing.T) {
	c, _ := newTestConvCache(t)

	got, err := c.Members(context.Background(), 900002)
	if err != nil {
		t.Fatalf("未命中不该报错: %v", err)
	}
	if got != nil {
		t.Fatalf("未命中应返 nil，实际 %v", got)
	}
}

// TestConversationCache_SetMembersOverwrites 重复回填是覆盖，不是追加。
func TestConversationCache_SetMembersOverwrites(t *testing.T) {
	c, rdb := newTestConvCache(t)
	ctx := context.Background()
	const convID = 900003

	t.Cleanup(func() { _ = rdb.Del(ctx, convMembersKey(convID)).Err() })

	c.SetMembers(ctx, convID, []uint64{1, 2, 3})
	c.SetMembers(ctx, convID, []uint64{7})

	got, err := c.Members(ctx, convID)
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	if len(got) != 1 || got[0] != 7 {
		t.Fatalf("回填应覆盖旧值，实际 %v", got)
	}
}

// TestConversationCache_InvalidateMembers 失效后读不到。
func TestConversationCache_InvalidateMembers(t *testing.T) {
	c, rdb := newTestConvCache(t)
	ctx := context.Background()
	const convID = 900004

	t.Cleanup(func() { _ = rdb.Del(ctx, convMembersKey(convID)).Err() })

	c.SetMembers(ctx, convID, []uint64{1, 2})
	c.InvalidateMembers(ctx, convID)

	got, err := c.Members(ctx, convID)
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	if got != nil {
		t.Fatalf("失效后应读不到，实际 %v", got)
	}
}

// TestConversationCache_SetMembersSetsTTL 回填的 key 有 TTL。
//
// 短 TTL 是漏删时的兜底 —— 成员变动时主动删，但万一漏了，TTL 到期自然清掉。
func TestConversationCache_SetMembersSetsTTL(t *testing.T) {
	c, rdb := newTestConvCache(t)
	ctx := context.Background()
	const convID = 900005

	t.Cleanup(func() { _ = rdb.Del(ctx, convMembersKey(convID)).Err() })

	c.SetMembers(ctx, convID, []uint64{1, 2})

	ttl, err := rdb.TTL(ctx, convMembersKey(convID)).Result()
	if err != nil {
		t.Fatalf("TTL: %v", err)
	}
	if ttl <= 0 {
		t.Fatalf("TTL = %v, want > 0（回填的 key 该有过期时间）", ttl)
	}
	if ttl > c.membersTTL {
		t.Fatalf("TTL = %v, want <= %v", ttl, c.membersTTL)
	}
}

// TestConversationCache_NilReceiverSafe nil cache 调用不 panic。
// Redis 不可用时 NewConversationCache 返 nil，repo 仍会调这些方法 ——
// 降级为每次查库，不能 panic。
func TestConversationCache_NilReceiverSafe(t *testing.T) {
	var c *ConversationCache
	ctx := context.Background()

	if got, err := c.Members(ctx, 1); err != nil || got != nil {
		t.Fatalf("nil cache Members 应返 (nil, nil)，实际 (%v, %v)", got, err)
	}
	// 这些方法没有返回值，panic 就是失败。
	c.SetMembers(ctx, 1, []uint64{1})
	c.InvalidateMembers(ctx, 1)
}
