package data

import (
	"context"
	"testing"

	"chatroom/app/user/service/internal/conf"
	"chatroom/pkg/keys"
	"chatroom/pkg/testutil"

	"github.com/redis/go-redis/v9"
)

// newTestRegisterLimiter 构造接真实 Redis 的注册限频器。
func newTestRegisterLimiter(t *testing.T) (*registerLimiter, *redis.Client) {
	t.Helper()
	rdb := testutil.NewRedis(t)
	ctx := context.Background()
	ks, err := rdb.Keys(ctx, keys.LimitBucketPattern("register:")).Result()
	if err == nil && len(ks) > 0 {
		rdb.Del(ctx, ks...)
	}
	limits := &conf.RateLimits{
		Register: &conf.RateLimit{
			Capacity: 3,
			Rate:     0.0001, // 速率极低 —— 测试里靠不到回充
		},
	}
	l := NewRegisterLimiter(&Data{rdb: rdb}, limits).(*registerLimiter)
	return l, rdb
}

// TestRegisterLimiter_AllowsUpToCapacity 桶里的令牌用完前一直放行。
func TestRegisterLimiter_AllowsUpToCapacity(t *testing.T) {
	l, _ := newTestRegisterLimiter(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := l.Allow(ctx, "1.2.3.4"); err != nil {
			t.Fatalf("第 %d 次注册应该放行: %v", i+1, err)
		}
	}
	// 第 4 次超配额
	if err := l.Allow(ctx, "1.2.3.4"); err == nil {
		t.Error("超配额应该被限流")
	}
}

// TestRegisterLimiter_ScopedByIP 不同 IP 的配额独立。
func TestRegisterLimiter_ScopedByIP(t *testing.T) {
	l, _ := newTestRegisterLimiter(t)
	ctx := context.Background()

	// 一个 IP 用光配额
	for i := 0; i < 3; i++ {
		l.Allow(ctx, "10.0.0.1")
	}
	if err := l.Allow(ctx, "10.0.0.1"); err == nil {
		t.Fatal("应该被限流")
	}

	// 别的 IP 不受影响
	if err := l.Allow(ctx, "203.0.113.7"); err != nil {
		t.Errorf("别的 IP 被牵连了: %v", err)
	}
}

// TestRegisterLimiter_EmptyIPStillLimited 空 IP 也限得住。
func TestRegisterLimiter_EmptyIPStillLimited(t *testing.T) {
	l, _ := newTestRegisterLimiter(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		l.Allow(ctx, "")
	}
	if err := l.Allow(ctx, ""); err == nil {
		t.Error("空 IP 也应该被限流")
	}
}

// TestRegisterLimiter_NilReceiverSafe nil 限频器不 panic。
func TestRegisterLimiter_NilReceiverSafe(t *testing.T) {
	var l *registerLimiter
	ctx := context.Background()
	if err := l.Allow(ctx, "1.2.3.4"); err != nil {
		t.Errorf("Allow: %v", err)
	}
}
