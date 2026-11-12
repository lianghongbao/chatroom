package data

import (
	"context"
	"testing"
	"time"

	"chatroom/app/user/service/internal/biz"
	"chatroom/app/user/service/internal/conf"
	"chatroom/pkg/testutil"

	"github.com/redis/go-redis/v9"
)

// newTestLoginLimiter 构造接真实 Redis 的登录限频器。
func newTestLoginLimiter(t *testing.T) (*loginLimiter, *redis.Client) {
	t.Helper()
	rdb := testutil.NewRedis(t)
	auth := &conf.Auth{}
	l := NewLoginLimiter(&Data{rdb: rdb}, auth).(*loginLimiter)
	return l, rdb
}

// TestLoginLimiter_LocksAfterMaxFail 连续失败达到阈值后锁定。
func TestLoginLimiter_LocksAfterMaxFail(t *testing.T) {
	l, _ := newTestLoginLimiter(t)
	ctx := context.Background()

	const maxFail = 5
	for i := 0; i < maxFail; i++ {
		l.OnFailure(ctx, "alice", "1.2.3.4")
	}
	if err := l.Allow(ctx, "alice", "1.2.3.4"); err == nil {
		t.Error("连续失败到阈值后应该被锁定")
	}
}

// TestLoginLimiter_IPDimensionPreventsLockoutDoS 攻击者不能锁掉别人的账号。
//
// 只按用户名锁的话，任何知道用户名的人试 5 次密码就能把账号锁 15 分钟。
// 加了 IP 维度之后，攻击者只能锁住「他自己这个 IP 上的该账号」，
// 账号本人从别的 IP 照样能登录。
func TestLoginLimiter_IPDimensionPreventsLockoutDoS(t *testing.T) {
	l, _ := newTestLoginLimiter(t)
	ctx := context.Background()

	// 攻击者从 10.0.0.1 反复试错
	for i := 0; i < 10; i++ {
		l.OnFailure(ctx, "victim", "10.0.0.1")
	}
	// 攻击者自己被锁住了
	if err := l.Allow(ctx, "victim", "10.0.0.1"); err == nil {
		t.Error("攻击者自己应该被锁")
	}

	// 受害者从别的 IP 登录 —— 不该被牵连
	if err := l.Allow(ctx, "victim", "203.0.113.7"); err != nil {
		t.Errorf("受害者从别的 IP 登录被拦了: %v —— 账号被 DoS 了", err)
	}
}

// TestLoginLimiter_OnSuccessClearsCounters 登录成功后清掉计数和锁定。
func TestLoginLimiter_OnSuccessClearsCounters(t *testing.T) {
	l, _ := newTestLoginLimiter(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		l.OnFailure(ctx, "alice", "1.2.3.4")
	}
	if err := l.Allow(ctx, "alice", "1.2.3.4"); err == nil {
		t.Fatal("应该被锁")
	}

	l.OnSuccess(ctx, "alice", "1.2.3.4")
	if err := l.Allow(ctx, "alice", "1.2.3.4"); err != nil {
		t.Errorf("登录成功后不该还被锁: %v", err)
	}
}

// TestLoginLimiter_EmptyIPStillWorks 没有 IP 时（内部调用 / gRPC）也要能限频。
func TestLoginLimiter_EmptyIPStillWorks(t *testing.T) {
	l, _ := newTestLoginLimiter(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		l.OnFailure(ctx, "alice", "")
	}
	if err := l.Allow(ctx, "alice", ""); err == nil {
		t.Error("空 IP 也应该被限频")
	}
}

// TestLoginLimiter_KeysAreScopedByUserAndIP 不同用户 / 不同 IP 的 key 不打架。
func TestLoginLimiter_KeysAreScopedByUserAndIP(t *testing.T) {
	l, rdb := newTestLoginLimiter(t)
	ctx := context.Background()

	l.OnFailure(ctx, "alice", "1.1.1.1")

	// alice 的计数存在自己的 key 上
	if rdb.Exists(ctx, l.failKey("alice", "1.1.1.1")).Val() != 1 {
		t.Error("alice 的失败计数 key 不存在")
	}
	// 别的用户名 / 别的 IP 不受影响
	if rdb.Exists(ctx, l.failKey("bob", "1.1.1.1")).Val() != 0 {
		t.Error("bob 的 key 被 alice 污染了")
	}
	if rdb.Exists(ctx, l.failKey("alice", "2.2.2.2")).Val() != 0 {
		t.Error("另一个 IP 的 key 被污染了")
	}
}

// TestLoginLimiter_FailCounterHasTTL 失败计数要有 TTL，不能永久累积。
func TestLoginLimiter_FailCounterHasTTL(t *testing.T) {
	l, rdb := newTestLoginLimiter(t)
	ctx := context.Background()

	l.OnFailure(ctx, "alice", "1.2.3.4")

	ttl, err := rdb.TTL(ctx, l.failKey("alice", "1.2.3.4")).Result()
	if err != nil {
		t.Fatalf("TTL: %v", err)
	}
	if ttl <= 0 || ttl > 20*time.Minute {
		t.Errorf("失败计数的 TTL = %v, want (0, 20m]", ttl)
	}
}

// TestLoginLimiter_NilReceiverSafe nil 限频器不 panic。
func TestLoginLimiter_NilReceiverSafe(t *testing.T) {
	var l *loginLimiter
	ctx := context.Background()
	if err := l.Allow(ctx, "a", "1.2.3.4"); err != nil {
		t.Errorf("Allow: %v", err)
	}
	l.OnFailure(ctx, "a", "1.2.3.4")
	l.OnSuccess(ctx, "a", "1.2.3.4")
	_ = biz.ErrUserLoginLocked
}
