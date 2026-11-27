package data

import (
	"context"
	"testing"
	"time"

	"chatroom/app/user/service/internal/biz"
	"chatroom/app/user/service/internal/conf"
	"chatroom/pkg/keys"
	"chatroom/pkg/testutil"

	"github.com/redis/go-redis/v9"
)

// newTestLoginLimiter 构造接真实 Redis 的登录限频器，返回限频器与 redis 客户端。
//
// 测试共用 DB 15，桶的 key 会跨用例残留 —— 上一次跑剩的桶会让
// 下一次从「桶已经空了」开始，测出莫名其妙的失败。所以清一遍。
func newTestLoginLimiter(t *testing.T) (*loginLimiter, *redis.Client) {
	t.Helper()
	rdb := testutil.NewRedis(t)
	ctx := context.Background()
	ks, err := rdb.Keys(ctx, keys.LimitBucketPattern("login:")).Result()
	if err == nil && len(ks) > 0 {
		rdb.Del(ctx, ks...)
	}
	auth := &conf.Auth{}
	l := NewLoginLimiter(&Data{rdb: rdb}, auth, nil).(*loginLimiter)
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

// TestLoginLimiter_LockTriggersExactlyAtThreshold 锁定的触发点不能差一次。
//
// 令牌桶的容量就是「允许的失败次数」。第 5 次失败把桶用完 → 锁定。
// 如果实现写成「桶空之后的那一次才锁」，就等于多失败一次才锁，
// 攻击者的试错次数多一次。这里守住精确边界。
func TestLoginLimiter_LockTriggersExactlyAtThreshold(t *testing.T) {
	l, _ := newTestLoginLimiter(t)
	ctx := context.Background()

	const maxFail = 5
	// 前 4 次失败不该锁
	for i := 0; i < maxFail-1; i++ {
		l.OnFailure(ctx, "bob", "1.2.3.4")
		if err := l.Allow(ctx, "bob", "1.2.3.4"); err != nil {
			t.Fatalf("第 %d 次失败后不该锁: %v", i+1, err)
		}
	}
	// 第 5 次失败正好触发锁定
	l.OnFailure(ctx, "bob", "1.2.3.4")
	if err := l.Allow(ctx, "bob", "1.2.3.4"); err == nil {
		t.Error("第 5 次失败后应该被锁定")
	}
}

// TestLoginLimiter_IPDimensionPreventsLockoutDoS 攻击者不能锁掉别人的账号。
// 加了 IP 维度后，攻击者只能锁住「他自己这个 IP 上的该账号」。
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

// TestLoginLimiter_OnSuccessRestoresFailureAllowance 登录成功后失败额度要补回来。
//
// 这是令牌桶相对固定窗口的核心优势：固定窗口的计数没法「减一」，
// 只能整窗清零 —— 一次成功登录会把整段时间的失败记录全抹掉。
// 令牌桶只还回一次失败的机会，其余失败记录保留。
func TestLoginLimiter_OnSuccessRestoresFailureAllowance(t *testing.T) {
	l, _ := newTestLoginLimiter(t)
	ctx := context.Background()

	// 失败 3 次（额度 5，剩 2）
	for i := 0; i < 3; i++ {
		l.OnFailure(ctx, "carol", "1.2.3.4")
	}
	// 成功一次 —— 额度补回 1，剩 3
	l.OnSuccess(ctx, "carol", "1.2.3.4")

	// 再失败 4 次不该锁（额度 3 + 补回的 1 = 4）
	for i := 0; i < 4; i++ {
		l.OnFailure(ctx, "carol", "1.2.3.4")
	}
	if err := l.Allow(ctx, "carol", "1.2.3.4"); err != nil {
		t.Errorf("成功登录后额度应该补回来，不该锁: %v", err)
	}
	// 第 5 次才锁
	l.OnFailure(ctx, "carol", "1.2.3.4")
	if err := l.Allow(ctx, "carol", "1.2.3.4"); err == nil {
		t.Error("额度用完应该被锁")
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

	aliceKey := keys.LimitBucketKey("login:fail", "alice|1.1.1.1")
	if rdb.Exists(ctx, aliceKey).Val() != 1 {
		t.Error("alice 的失败计数桶不存在")
	}
	// 别的用户名 / 别的 IP 不受影响
	if rdb.Exists(ctx, keys.LimitBucketKey("login:fail", "bob|1.1.1.1")).Val() != 0 {
		t.Error("bob 的桶被 alice 污染了")
	}
	if rdb.Exists(ctx, keys.LimitBucketKey("login:fail", "alice|2.2.2.2")).Val() != 0 {
		t.Error("另一个 IP 的桶被污染了")
	}
}

// TestLoginLimiter_FailBucketHasTTL 失败计数要有 TTL，不能永久累积。
func TestLoginLimiter_FailBucketHasTTL(t *testing.T) {
	l, rdb := newTestLoginLimiter(t)
	ctx := context.Background()

	l.OnFailure(ctx, "alice", "1.2.3.4")

	ttl, err := rdb.TTL(ctx, keys.LimitBucketKey("login:fail", "alice|1.2.3.4")).Result()
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
