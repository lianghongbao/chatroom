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
	for _, scope := range []string{"register:", "check_username:"} {
		ks, err := rdb.Keys(ctx, keys.LimitBucketPattern(scope)).Result()
		if err == nil && len(ks) > 0 {
			rdb.Del(ctx, ks...)
		}
	}
	limits := &conf.RateLimits{
		Register: &conf.RateLimit{
			Capacity: 3,
			Rate:     0.0001, // 速率极低 —— 测试里靠不到回充
		},
		CheckUsername: &conf.RateLimit{
			Capacity: 3,
			Rate:     0.0001,
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
	if err := l.AllowCheck(ctx, "1.2.3.4"); err != nil {
		t.Errorf("AllowCheck: %v", err)
	}
}

// TestRegisterLimiter_CheckDoesNotConsumeRegisterQuota 校验请求不消耗注册配额。
//
// 这是整个「分开两个桶」设计的核心不变量。
//
// 注册页的输入防抖 + 失焦会连发好几次校验 —— 如果校验和注册共用一个桶，
// 用户光是在输入框里打字就能把自己的注册名额耗光，
// 最后提交注册时被 429 拒掉，而且用户完全不知道为什么。
func TestRegisterLimiter_CheckDoesNotConsumeRegisterQuota(t *testing.T) {
	l, _ := newTestRegisterLimiter(t)
	ctx := context.Background()

	// 用光校验桶
	for i := 0; i < 3; i++ {
		if err := l.AllowCheck(ctx, "1.2.3.4"); err != nil {
			t.Fatalf("第 %d 次校验应该放行: %v", i+1, err)
		}
	}
	if err := l.AllowCheck(ctx, "1.2.3.4"); err == nil {
		t.Fatal("校验超配额应该被限流")
	}

	// 注册配额必须纹丝不动
	for i := 0; i < 3; i++ {
		if err := l.Allow(ctx, "1.2.3.4"); err != nil {
			t.Errorf("第 %d 次注册应该放行 —— 校验不该消耗注册配额: %v", i+1, err)
		}
	}
	if err := l.Allow(ctx, "1.2.3.4"); err == nil {
		t.Error("注册超配额应该被限流")
	}
}

// TestRegisterLimiter_RegisterDoesNotConsumeCheckQuota 反向也不互相影响。
func TestRegisterLimiter_RegisterDoesNotConsumeCheckQuota(t *testing.T) {
	l, _ := newTestRegisterLimiter(t)
	ctx := context.Background()

	// 用光注册桶
	for i := 0; i < 3; i++ {
		l.Allow(ctx, "1.2.3.4")
	}
	if err := l.Allow(ctx, "1.2.3.4"); err == nil {
		t.Fatal("注册超配额应该被限流")
	}

	// 校验配额不受影响
	for i := 0; i < 3; i++ {
		if err := l.AllowCheck(ctx, "1.2.3.4"); err != nil {
			t.Errorf("第 %d 次校验应该放行 —— 注册不该消耗校验配额: %v", i+1, err)
		}
	}
}

// TestRegisterLimiter_CheckUsesSeparateScope 两个桶的 Redis key 不同。
func TestRegisterLimiter_CheckUsesSeparateScope(t *testing.T) {
	registerKey := keys.LimitBucketKey("register", "1.2.3.4")
	checkKey := keys.LimitBucketKey("check_username", "1.2.3.4")
	if registerKey == checkKey {
		t.Fatalf("两个 scope 生成了同一个 key: %s", registerKey)
	}
}
