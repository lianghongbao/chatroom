package limiter

import (
	"context"
	"sync"
	"testing"
	"time"

	"chatroom/pkg/testutil"

	"github.com/redis/go-redis/v9"
)

// newTestBucket 构造接真实 Redis 的令牌桶，返回桶与 redis 客户端。
//
// 测试共用 DB 15，桶的 key 会跨用例残留 —— 上一次跑剩的桶会让
// 下一次从「桶已经空了」开始，测出莫名其妙的失败。
// 所以这里清一遍所有测试前缀的 key。
func newTestBucket(t *testing.T, opts ...Option) (*Bucket, *redis.Client) {
	t.Helper()
	rdb := testutil.NewRedis(t)
	ctx := context.Background()
	keys, err := rdb.Keys(ctx, "test:bucket:*").Result()
	if err == nil {
		if len(keys) > 0 {
			rdb.Del(ctx, keys...)
		}
	}
	return New(rdb, opts...), rdb
}

// TestBucket_ConsumesUpToCapacity 桶里的令牌用完前一直放行。
func TestBucket_ConsumesUpToCapacity(t *testing.T) {
	b, _ := newTestBucket(t, WithCapacity(5), WithRate(0.0001))
	ctx := context.Background()
	key := "test:bucket:consume"

	for i := 0; i < 5; i++ {
		if res := b.Allow(ctx, key, time.Time{}); !res.Allowed {
			t.Fatalf("第 %d 次应该放行", i+1)
		}
	}
	// 第 6 次桶空了 —— 速率极低，不会回充
	if res := b.Allow(ctx, key, time.Time{}); res.Allowed {
		t.Error("桶空后应该拒绝")
	}
}

// TestBucket_RefillsOverTime 速率决定回充：等一会儿桶里又有令牌了。
func TestBucket_RefillsOverTime(t *testing.T) {
	// 速率 20/秒 = 每 50ms 补 1 个
	b, _ := newTestBucket(t, WithCapacity(2), WithRate(20))
	ctx := context.Background()
	key := "test:bucket:refill"

	// 先耗光
	b.Allow(ctx, key, time.Time{})
	b.Allow(ctx, key, time.Time{})
	if res := b.Allow(ctx, key, time.Time{}); res.Allowed {
		t.Fatal("应该已经用光")
	}

	time.Sleep(120 * time.Millisecond)

	if res := b.Allow(ctx, key, time.Time{}); !res.Allowed {
		t.Errorf("等 120ms 后应该回充到有令牌: %+v", res)
	}
}

// TestBucket_PeekDoesNotConsume Peek 只看不拿。
func TestBucket_PeekDoesNotConsume(t *testing.T) {
	b, _ := newTestBucket(t, WithCapacity(3), WithRate(0.0001))
	ctx := context.Background()
	key := "test:bucket:peek"

	first := b.Peek(ctx, key, time.Time{})
	if !first.Allowed {
		t.Fatal("满桶应该 allowed")
	}
	if first.Remaining != 3 {
		t.Errorf("Remaining = %d, want 3", first.Remaining)
	}

	// 连看三次，每次都应该是 3
	for i := 0; i < 3; i++ {
		res := b.Peek(ctx, key, time.Time{})
		if res.Remaining != 3 {
			t.Errorf("Peek 消耗了令牌: Remaining = %d, want 3", res.Remaining)
		}
	}
}

// TestBucket_AllowNAllOrNothing 要么全给，要么不给。
func TestBucket_AllowNAllOrNothing(t *testing.T) {
	b, _ := newTestBucket(t, WithCapacity(3), WithRate(0.0001))
	ctx := context.Background()
	key := "test:bucket:allornothing"

	// 桶里 3 个，要 2 个 —— 给
	if res := b.AllowN(ctx, key, 2, time.Time{}); !res.Allowed {
		t.Error("应该放行")
	}
	// 剩 1 个，要 2 个 —— 不给，且不消耗
	res := b.AllowN(ctx, key, 2, time.Time{})
	if res.Allowed {
		t.Error("不够就不该放行")
	}
	// 剩的令牌还在
	if peek := b.Peek(ctx, key, time.Time{}); peek.Remaining != 1 {
		t.Errorf("失败的 AllowN 消耗了令牌: Remaining = %d, want 1", peek.Remaining)
	}
}

// TestBucket_RefillRestoresCapacity Refill 把桶补满。
func TestBucket_RefillRestoresCapacity(t *testing.T) {
	b, _ := newTestBucket(t, WithCapacity(3), WithRate(0.0001))
	ctx := context.Background()
	key := "test:bucket:refillFull"

	b.Allow(ctx, key, time.Time{})
	b.Allow(ctx, key, time.Time{})
	if peek := b.Peek(ctx, key, time.Time{}); peek.Remaining != 1 {
		t.Fatalf("消耗后 Remaining = %d, want 1", peek.Remaining)
	}

	b.Refill(ctx, key, time.Time{})
	if peek := b.Peek(ctx, key, time.Time{}); peek.Remaining != 3 {
		t.Errorf("Refill 后 Remaining = %d, want 3", peek.Remaining)
	}
}

// TestBucket_ResetDeletesBucket Reset 删掉桶。
func TestBucket_ResetDeletesBucket(t *testing.T) {
	b, rdb := newTestBucket(t, WithCapacity(3))
	ctx := context.Background()
	key := "test:bucket:reset"

	b.Allow(ctx, key, time.Time{})
	b.Reset(ctx, key)

	if exists := rdb.Exists(ctx, key).Val(); exists != 0 {
		t.Errorf("Reset 后 key 还在: exists = %d", exists)
	}
}

// TestBucket_KeyHasTTL 桶要能自动回收，不能留永久垃圾 key。
func TestBucket_KeyHasTTL(t *testing.T) {
	b, rdb := newTestBucket(t, WithCapacity(3), WithTTL(5*time.Second))
	ctx := context.Background()
	key := "test:bucket:ttl"

	b.Allow(ctx, key, time.Time{})

	ttl, err := rdb.TTL(ctx, key).Result()
	if err != nil {
		t.Fatalf("TTL: %v", err)
	}
	if ttl <= 0 || ttl > 10*time.Second {
		t.Errorf("TTL = %v, want (0, 10s]", ttl)
	}
}

// TestBucket_ConcurrentConsumeIsAtomic 并发取令牌不能超发。
//
// 这是用 Lua 的全部意义所在：非原子的话两个请求会读到同一个余量、
// 双双通过，实际放行量翻倍。
func TestBucket_ConcurrentConsumeIsAtomic(t *testing.T) {
	const capacity = 20
	b, _ := newTestBucket(t, WithCapacity(capacity), WithRate(0.0001))
	ctx := context.Background()
	key := "test:bucket:concurrent"

	const goroutines = 50
	const perGoroutine = 10

	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := 0
			for j := 0; j < perGoroutine; j++ {
				if b.Allow(ctx, key, time.Time{}).Allowed {
					local++
				}
			}
			mu.Lock()
			allowed += local
			mu.Unlock()
		}()
	}
	wg.Wait()

	if allowed != capacity {
		t.Errorf("并发取令牌放行了 %d 次, want %d（超发说明脚本不原子）", allowed, capacity)
	}
}

// TestBucket_NilRedisDegradesToAllow Redis 不可达时放行。
func TestBucket_NilRedisDegradesToAllow(t *testing.T) {
	b := New(nil, WithCapacity(1))
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		if !b.Allow(ctx, "test:bucket:nil", time.Time{}).Allowed {
			t.Fatal("Redis 不可达时应该放行")
		}
	}
}

// TestBucket_NilReceiverSafe nil 桶不 panic。
func TestBucket_NilReceiverSafe(t *testing.T) {
	var b *Bucket
	ctx := context.Background()
	if !b.Allow(ctx, "k", time.Time{}).Allowed {
		t.Error("nil 桶应该放行")
	}
	b.Refill(ctx, "k", time.Time{})
	b.Reset(ctx, "k")
	if !b.Peek(ctx, "k", time.Time{}).Allowed {
		t.Error("nil 桶 Peek 应该放行")
	}
}

// TestBucket_ExplicitTimeIsUsed 传固定时间戳时脚本用它 —— 测试要可重现。
func TestBucket_ExplicitTimeIsUsed(t *testing.T) {
	b, _ := newTestBucket(t, WithCapacity(2), WithRate(1))
	ctx := context.Background()
	key := "test:bucket:explicitTime"

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	b.Allow(ctx, key, base)
	b.Allow(ctx, key, base)

	// 1 秒后（速率 1/秒）应该回充 1 个
	later := base.Add(time.Second)
	if res := b.Allow(ctx, key, later); !res.Allowed {
		t.Errorf("1 秒后应该回充: %+v", res)
	}
}
