package data

import (
	"context"
	"testing"

	"chatroom/pkg/keys"
	"chatroom/pkg/testutil"

	"github.com/redis/go-redis/v9"
)

// newTestFriendLimiter 构造接真实 Redis 的好友限频器。
//
// 测试共用 DB 15，桶的 key 会跨用例残留 —— 上一轮跑剩的桶会让
// 这一轮从「桶已经空了」开始，测出莫名其妙的失败。所以清一遍。
func newTestFriendLimiter(t *testing.T) (*FriendLimiter, *redis.Client) {
	t.Helper()
	rdb := testutil.NewRedis(t)
	ctx := context.Background()
	ks, err := rdb.Keys(ctx, keys.LimitBucketPattern("friend:")).Result()
	if err == nil && len(ks) > 0 {
		rdb.Del(ctx, ks...)
	}
	return NewFriendLimiter(&Data{rdb: rdb}).(*FriendLimiter), rdb
}

// TestFriendLimiter_ApplyLimitedAfterCapacity 申请超容量被限流。
func TestFriendLimiter_ApplyLimitedAfterCapacity(t *testing.T) {
	l, _ := newTestFriendLimiter(t)
	ctx := context.Background()

	// 容量 30 —— 前 30 次放行
	for i := 0; i < 30; i++ {
		if err := l.AllowApply(ctx, "u1"); err != nil {
			t.Fatalf("第 %d 次申请应该放行: %v", i+1, err)
		}
	}
	// 第 31 次超配额
	if err := l.AllowApply(ctx, "u1"); err == nil {
		t.Error("超配额应该被限流")
	}
}

// TestFriendLimiter_ScopedByCaller 不同调用方的配额独立。
func TestFriendLimiter_ScopedByCaller(t *testing.T) {
	l, _ := newTestFriendLimiter(t)
	ctx := context.Background()

	for i := 0; i < 30; i++ {
		l.AllowApply(ctx, "u1")
	}
	if err := l.AllowApply(ctx, "u1"); err == nil {
		t.Fatal("u1 应该被限流")
	}
	// 别的用户不受影响
	if err := l.AllowApply(ctx, "u2"); err != nil {
		t.Errorf("u2 被牵连了: %v", err)
	}
}

// TestFriendLimiter_OperationsHaveSeparateBuckets 不同动作的配额独立。
func TestFriendLimiter_OperationsHaveSeparateBuckets(t *testing.T) {
	l, _ := newTestFriendLimiter(t)
	ctx := context.Background()

	// 把申请配额用光
	for i := 0; i < 30; i++ {
		l.AllowApply(ctx, "u1")
	}
	if err := l.AllowApply(ctx, "u1"); err == nil {
		t.Fatal("申请应该被限流")
	}
	// 同意 / 拒绝是另一个桶
	if err := l.AllowAcceptReject(ctx, "u1"); err != nil {
		t.Errorf("同意被牵连了: %v", err)
	}
	// 删除是另一个桶
	if err := l.AllowRemove(ctx, "u1"); err != nil {
		t.Errorf("删除被牵连了: %v", err)
	}
}

// TestFriendLimiter_EmptyCallerGetsOwnBucket 空 caller 有自己的桶。
//
// 不能让所有空 caller 共用一个桶 —— 那会变成一个全局配额，
// 一个空 id 的请求就能把所有人挡了。
func TestFriendLimiter_EmptyCallerGetsOwnBucket(t *testing.T) {
	l, rdb := newTestFriendLimiter(t)
	ctx := context.Background()

	for i := 0; i < 30; i++ {
		l.AllowApply(ctx, "")
	}
	if err := l.AllowApply(ctx, ""); err == nil {
		t.Error("空 caller 也应该被限流")
	}

	// key 用占位符，不是空串
	key := keys.LimitBucketKey("friend:apply", "")
	if rdb.Exists(ctx, key).Val() != 1 {
		t.Errorf("占位符 key 不存在: %s", key)
	}
}

// TestFriendLimiter_NilReceiverSafe nil 限频器不 panic。
func TestFriendLimiter_NilReceiverSafe(t *testing.T) {
	var l *FriendLimiter
	ctx := context.Background()
	if err := l.AllowApply(ctx, "u1"); err != nil {
		t.Errorf("AllowApply: %v", err)
	}
	if err := l.AllowAcceptReject(ctx, "u1"); err != nil {
		t.Errorf("AllowAcceptReject: %v", err)
	}
	if err := l.AllowRemove(ctx, "u1"); err != nil {
		t.Errorf("AllowRemove: %v", err)
	}
}
