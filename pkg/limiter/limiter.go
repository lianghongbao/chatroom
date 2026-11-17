// Package limiter 提供基于 Redis + Lua 的令牌桶限流器。
package limiter

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// takeTokenScript 令牌桶的取令牌脚本。
//
// KEYS[1] 桶 key
// ARGV[1] capacity 桶容量（令牌总数）
// ARGV[2] refillRate 每秒补充令牌数（浮点）
// ARGV[3] nowMs 当前时间（毫秒，客户端传，避免 Lua 里的 TIME 在
//        集群/副本模式下不一致）
// ARGV[4] ttlMs 桶的空闲回收时间（毫秒）—— 满了就不再续期
//
// 返回 {allowed, remaining, retryAfterMs}：
//   - allowed 0/1，是否放行
//   - remaining 剩余令牌数（浮点，Redis 返回的是整数化后的）
//   - retryAfterMs 桶空时，距下一个令牌到达的毫秒数
//
// 桶的存储用 hash：字段 tokens 存余量，字段 ts 存上次补充的时间戳。
// 单个 hash 而不是两个 string —— 免得「读 tokens」和「读 ts」之间
// 出现时间差。
const takeTokenScript = `
local key = KEYS[1]
local capacity = tonumber(ARGV[1])
local refillRate = tonumber(ARGV[2])
local nowMs = tonumber(ARGV[3])
local ttlMs = tonumber(ARGV[4])

local data = redis.call('HMGET', key, 'tokens', 'ts')
local tokens = tonumber(data[1])
local ts = tonumber(data[2])

if tokens == nil or ts == nil then
  -- 桶不存在（从没来过，或已被回收）：满桶开始。
  tokens = capacity
  ts = nowMs
end

-- 补充令牌：距离上次补充过了 elapsedMs，就按速率补。
local elapsedMs = nowMs - ts
if elapsedMs < 0 then
  -- 时钟回拨（客户端机器时间不一致）。当成 0，不补也不退。
  elapsedMs = 0
end
if refillRate > 0 then
  tokens = tokens + (elapsedMs / 1000.0) * refillRate
  if tokens > capacity then
    tokens = capacity
  end
end

local allowed = 0
local retryAfterMs = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
else
  -- 桶空：算出还要等多久才有下一个令牌。
  if refillRate > 0 then
    retryAfterMs = math.ceil(((1 - tokens) / refillRate) * 1000)
  else
    -- 速率 0 = 完全不补令牌。这种情况永远不该有令牌可用。
    retryAfterMs = -1
  end
end

redis.call('HSET', key, 'tokens', tokens, 'ts', nowMs)
-- PEXPIRE 无条件设置：桶的空闲回收靠它。
-- 每次访问都续期 —— 有人在用的桶不会消失，没人用的桶到期自清。
redis.call('PEXPIRE', key, ttlMs)

return {allowed, math.floor(tokens), retryAfterMs}
`

// peekTokenScript 只补充、不消耗的版本。
//
// 参数和 takeTokenScript 一样。返回 {remaining, retryAfterMs}。
//
// 「补充」这一步必须做 —— 否则桶里的存量会停在旧值，
// 看到的是一个过期的余量。
const peekTokenScript = `
local key = KEYS[1]
local capacity = tonumber(ARGV[1])
local refillRate = tonumber(ARGV[2])
local nowMs = tonumber(ARGV[3])
local ttlMs = tonumber(ARGV[4])

local data = redis.call('HMGET', key, 'tokens', 'ts')
local tokens = tonumber(data[1])
local ts = tonumber(data[2])

if tokens == nil or ts == nil then
  tokens = capacity
  ts = nowMs
end

local elapsedMs = nowMs - ts
if elapsedMs < 0 then
  elapsedMs = 0
end
if refillRate > 0 then
  tokens = tokens + (elapsedMs / 1000.0) * refillRate
  if tokens > capacity then
    tokens = capacity
  end
end

local retryAfterMs = 0
if tokens < 1 then
  if refillRate > 0 then
    retryAfterMs = math.ceil(((1 - tokens) / refillRate) * 1000)
  else
    retryAfterMs = -1
  end
end

redis.call('HSET', key, 'tokens', tokens, 'ts', nowMs)
redis.call('PEXPIRE', key, ttlMs)

return {math.floor(tokens), retryAfterMs}
`

// Result 是一次取令牌的结果。
type Result struct {
	// Allowed 是否放行。
	Allowed bool
	// Remaining 取完后的剩余令牌数（向下取整）。
	Remaining int64
	// RetryAfterMs 桶空时距下一个令牌的毫秒数；-1 表示速率为 0，永远等不到。
	RetryAfterMs int64
}

// Bucket 令牌桶限流器。
//
// 同一个 Bucket 实例可以被多个 goroutine 并发使用 ——
// 所有状态都在 Redis 里，本地只有一个不可变的参数。
type Bucket struct {
	rdb redis.Cmdable
	// capacity 桶容量。一次最多能拿走 capacity 个令牌。
	capacity float64
	// refillRate 每秒补充的令牌数。0.5 = 每 2 秒补 1 个。
	refillRate float64
	// ttl 桶的空闲回收时间。桶空了就不再被访问时，靠它自动清理。
	ttl time.Duration
}

// Option 配置 Bucket。
type Option func(*Bucket)

// WithCapacity 设桶容量。<= 0 时用默认值 1。
func WithCapacity(capacity float64) Option {
	return func(b *Bucket) {
		if capacity > 0 {
			b.capacity = capacity
		}
	}
}

// WithRate 设每秒补充的令牌数。<= 0 时用默认值 1。
func WithRate(rate float64) Option {
	return func(b *Bucket) {
		if rate > 0 {
			b.refillRate = rate
		}
	}
}

// WithTTL 设桶的空闲回收时间。<= 0 时用默认值 1 分钟。
func WithTTL(ttl time.Duration) Option {
	return func(b *Bucket) {
		if ttl > 0 {
			b.ttl = ttl
		}
	}
}

// New 构造令牌桶限流器。
//
// rdb Redis 客户端，为 nil 时所有 Allow 都放行（降级）。
// opts 依次应用；不传的项用默认值：容量 1、速率 1/秒、TTL 1 分钟。
func New(rdb redis.Cmdable, opts ...Option) *Bucket {
	b := &Bucket{
		rdb:        rdb,
		capacity:   1,
		refillRate: 1,
		ttl:        time.Minute,
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// Allow 取一个令牌。
//
// key 桶的 Redis key —— 不同维度用不同 key。
// now 取令牌用的时间戳，零值时用本地时钟。测试可以传固定值。
//
// 返回结果。Redis 不可达时返回 {Allowed: true}（降级放行）——
// 限流是保护措施，Redis 抖一下不该把业务全拒了。
func (b *Bucket) Allow(ctx context.Context, key string, now time.Time) Result {
	if b == nil || b.rdb == nil {
		return Result{Allowed: true}
	}
	nowMs := now.UnixMilli()
	if now.IsZero() {
		nowMs = time.Now().UnixMilli()
	}
	vals, err := b.rdb.Eval(ctx, takeTokenScript,
		[]string{key},
		b.capacity, b.refillRate, nowMs, b.ttl.Milliseconds(),
	).Slice()
	if err != nil {
		// 降级放行。调用方想记日志就在自己那层记。
		return Result{Allowed: true}
	}
	if len(vals) < 3 {
		return Result{Allowed: true}
	}
	allowed, _ := vals[0].(int64)
	remaining, _ := vals[1].(int64)
	retryAfter, _ := vals[2].(int64)
	return Result{
		Allowed:      allowed == 1,
		Remaining:    remaining,
		RetryAfterMs: retryAfter,
	}
}

// AllowN 取 n 个令牌。
//
// 拿不满就一个都不拿（all-or-nothing）—— 部分放行会让调用方
// 拿到「半个配额」，不知道该怎么办。
//
// 实现：先 Peek 看余额够不够，不够就直接拒（不消耗）。
// 够的话再逐个取 —— 取的过程中可能被并发请求抢走，
// 这时只能返回「没拿到」并如实报告已消耗的数目。
// 业务上 n 几乎都是 1，这个竞争窗口可以忽略。
//
// n <= 0 时直接放行（不消耗令牌）。
func (b *Bucket) AllowN(ctx context.Context, key string, n int, now time.Time) Result {
	if n <= 0 {
		return Result{Allowed: true}
	}
	if b == nil || b.rdb == nil {
		return Result{Allowed: true}
	}
	// 余额不够就不动桶 —— 保证「要么全给要么全不给」。
	if peek := b.Peek(ctx, key, now); peek.Remaining < int64(n) {
		return Result{
			Allowed:      false,
			Remaining:    peek.Remaining,
			RetryAfterMs: peek.RetryAfterMs,
		}
	}
	for i := 0; i < n; i++ {
		r := b.Allow(ctx, key, now)
		if !r.Allowed {
			return r
		}
	}
	return Result{Allowed: true}
}

// Peek 看桶里还有多少令牌，**不消耗**。
//
// 用途：计数型限流 —— 「失败次数」这类场景要先看有没有超配额，
// 超了才记一次并触发锁定。但「看」和「扣」必须原子，
// 否则两个并发失败会同时看到还有余额、双双通过。
//
// 返回剩余令牌数。Redis 不可达时返 capacity（视为满桶），
// 因为降级语义是放行。
func (b *Bucket) Peek(ctx context.Context, key string, now time.Time) Result {
	if b == nil || b.rdb == nil {
		return Result{Allowed: true, Remaining: int64(b.Capacity())}
	}
	nowMs := now.UnixMilli()
	if now.IsZero() {
		nowMs = time.Now().UnixMilli()
	}
	vals, err := b.rdb.Eval(ctx, peekTokenScript,
		[]string{key},
		b.capacity, b.refillRate, nowMs, b.ttl.Milliseconds(),
	).Slice()
	if err != nil {
		return Result{Allowed: true, Remaining: int64(b.Capacity())}
	}
	// peek 脚本返回 {remaining, retryAfterMs} —— 两个元素。
	if len(vals) < 2 {
		return Result{Allowed: true, Remaining: int64(b.Capacity())}
	}
	remaining, _ := vals[0].(int64)
	retryAfter, _ := vals[1].(int64)
	return Result{
		Allowed:      remaining >= 1,
		Remaining:    remaining,
		RetryAfterMs: retryAfter,
	}
}

// Refill 按速率把桶补满（不消耗令牌）。
//
// 用途：登录成功时清掉失败计数 —— 令牌桶的等价操作是「把桶补满」，
// 因为桶的存量就是「还没被用掉的失败次数」。
func (b *Bucket) Refill(ctx context.Context, key string, now time.Time) {
	if b == nil || b.rdb == nil {
		return
	}
	nowMs := now.UnixMilli()
	if now.IsZero() {
		nowMs = time.Now().UnixMilli()
	}
	const refillScript = `
local key = KEYS[1]
local capacity = tonumber(ARGV[1])
local nowMs = tonumber(ARGV[2])
local ttlMs = tonumber(ARGV[3])
redis.call('HSET', key, 'tokens', capacity, 'ts', nowMs)
redis.call('PEXPIRE', key, ttlMs)
return 1
`
	if err := b.rdb.Eval(ctx, refillScript,
		[]string{key}, b.capacity, nowMs, b.ttl.Milliseconds(),
	).Err(); err != nil {
		// 补桶失败不影响主流程 —— 顶多下次还限着。
		return
	}
}

// Reset 删掉桶（等价于「计数清零」）。
//
// 桶不存在时是幂等的 —— DEL 返 0 也算成功。
func (b *Bucket) Reset(ctx context.Context, key string) {
	if b == nil || b.rdb == nil {
		return
	}
	b.rdb.Del(ctx, key)
}

// Capacity 返回桶容量。
func (b *Bucket) Capacity() float64 {
	if b == nil {
		return 0
	}
	return b.capacity
}

// Rate 返回每秒补充的令牌数。
func (b *Bucket) Rate() float64 {
	if b == nil {
		return 0
	}
	return b.refillRate
}
