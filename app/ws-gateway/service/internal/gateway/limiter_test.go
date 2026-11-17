package gateway

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// TestHandshakeLimiter_AllowsUpToBurst 突发额度内一直放行。
func TestHandshakeLimiter_AllowsUpToBurst(t *testing.T) {
	l := newHandshakeLimiter(20, 40)

	for i := 0; i < 40; i++ {
		if !l.Allow("1.2.3.4") {
			t.Fatalf("第 %d 次握手应该放行", i+1)
		}
	}
	// 突发额度用完
	if l.Allow("1.2.3.4") {
		t.Error("突发额度用完后应该拒绝")
	}
}

// TestHandshakeLimiter_ScopedByIP 不同 IP 独立。
func TestHandshakeLimiter_ScopedByIP(t *testing.T) {
	l := newHandshakeLimiter(20, 40)

	for i := 0; i < 40; i++ {
		l.Allow("1.2.3.4")
	}
	if l.Allow("1.2.3.4") {
		t.Fatal("应该被限流")
	}
	// 别的 IP 不受影响
	if !l.Allow("5.6.7.8") {
		t.Error("别的 IP 被牵连了")
	}
}

// TestHandshakeLimiter_RefillsOverTime 速率决定回充。
func TestHandshakeLimiter_RefillsOverTime(t *testing.T) {
	// 速率 100/秒 —— 等 50ms 就回充 5 个
	l := newHandshakeLimiter(100, 1)

	// 用光
	if !l.Allow("1.2.3.4") {
		t.Fatal("第一次应该放行")
	}
	if l.Allow("1.2.3.4") {
		t.Fatal("应该被限流")
	}

	time.Sleep(60 * time.Millisecond)

	if !l.Allow("1.2.3.4") {
		t.Error("等 60ms 后应该回充")
	}
}

// TestHandshakeLimiter_ConcurrentIsAtomic 并发握手不能超发。
func TestHandshakeLimiter_ConcurrentIsAtomic(t *testing.T) {
	const burst = 50
	l := newHandshakeLimiter(1, burst)

	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := 0
			for j := 0; j < 10; j++ {
				if l.Allow("1.2.3.4") {
					local++
				}
			}
			mu.Lock()
			allowed += local
			mu.Unlock()
		}()
	}
	wg.Wait()

	if allowed > burst {
		t.Errorf("并发握手放行了 %d 次, want <= %d", allowed, burst)
	}
}

// TestHandshakeLimiter_NilReceiverSafe nil 限流器不 panic。
func TestHandshakeLimiter_NilReceiverSafe(t *testing.T) {
	var l *handshakeLimiter
	if !l.Allow("1.2.3.4") {
		t.Error("nil 限流器应该放行")
	}
}

// TestConnectionLimiter_RespectsTotalLimit 总连接数上限。
func TestConnectionLimiter_RespectsTotalLimit(t *testing.T) {
	l := newConnectionLimiter(3, 10)

	for i := 0; i < 3; i++ {
		if err := l.reserve("u1"); err != nil {
			t.Fatalf("第 %d 次预留应该成功: %v", i+1, err)
		}
	}
	// 第 4 次超总配额
	err := l.reserve("u2")
	if !errors.Is(err, errConnLimitTotal) {
		t.Errorf("超总配额应该返 errConnLimitTotal, got %v", err)
	}
}

// TestConnectionLimiter_RespectsPerUserLimit 单用户连接数上限。
func TestConnectionLimiter_RespectsPerUserLimit(t *testing.T) {
	l := newConnectionLimiter(100, 2)

	if err := l.reserve("u1"); err != nil {
		t.Fatal("第一次应该成功")
	}
	if err := l.reserve("u1"); err != nil {
		t.Fatal("第二次应该成功")
	}
	// 第 3 次超单用户配额
	err := l.reserve("u1")
	if !errors.Is(err, errConnLimitUser) {
		t.Errorf("超单用户配额应该返 errConnLimitUser, got %v", err)
	}
	// 别的用户不受影响
	if err := l.reserve("u2"); err != nil {
		t.Errorf("别的用户被牵连了: %v", err)
	}
}

// TestConnectionLimiter_ReleaseFreesQuota 归还配额后能再预留。
func TestConnectionLimiter_ReleaseFreesQuota(t *testing.T) {
	l := newConnectionLimiter(1, 1)

	if err := l.reserve("u1"); err != nil {
		t.Fatal("第一次应该成功")
	}
	if err := l.reserve("u2"); err == nil {
		t.Error("配额已满应该拒绝")
	}
	l.release("u1")
	if err := l.reserve("u2"); err != nil {
		t.Errorf("归还配额后应该能预留: %v", err)
	}
}

// TestConnectionLimiter_ReleaseIsIdempotent 重复归还不能把计数搞成负数。
func TestConnectionLimiter_ReleaseIsIdempotent(t *testing.T) {
	l := newConnectionLimiter(2, 2)

	l.reserve("u1")
	l.release("u1")
	l.release("u1")
	l.release("u1")

	// 配额应该还是满的（没被释放成负数）
	if err := l.reserve("u1"); err != nil {
		t.Errorf("重复释放把配额算坏了: %v", err)
	}
	if err := l.reserve("u2"); err != nil {
		t.Errorf("重复释放把配额算坏了: %v", err)
	}
}

// TestConnectionLimiter_ConcurrentReserveIsAtomic 并发预留不能超发。
func TestConnectionLimiter_ConcurrentReserveIsAtomic(t *testing.T) {
	const total = 20
	l := newConnectionLimiter(total, 100)

	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := 0
			for j := 0; j < 10; j++ {
				if err := l.reserve("u1"); err == nil {
					local++
				}
			}
			mu.Lock()
			ok += local
			mu.Unlock()
		}()
	}
	wg.Wait()

	if ok != total {
		t.Errorf("并发预留成功 %d 次, want %d", ok, total)
	}
}

// TestConnectionLimiter_NilReceiverSafe nil 配额器不 panic。
func TestConnectionLimiter_NilReceiverSafe(t *testing.T) {
	var l *connectionLimiter
	if err := l.reserve("u1"); err != nil {
		t.Error("nil 配额器应该放行")
	}
	l.release("u1")
}
