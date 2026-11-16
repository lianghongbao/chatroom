// Package breaker 提供跨服务 gRPC 调用的熔断中间件。
// 按 operation 粒度统计（Kratos 内部按 gRPC method 分组）。
package breaker

import (
	"errors"
	"sync"
	"time"

	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/middleware/circuitbreaker"
)

// ErrNotAllowed 熔断打开，请求被拒。
var ErrNotAllowed = errors.New("breaker: request rejected, circuit is open")

// 默认参数。
const (
	// DefaultFailureRatio 触发熔断的失败率阈值。
	DefaultFailureRatio = 0.5
	DefaultRequest      = 20
	DefaultWindow       = 10 * time.Second
	DefaultBucket       = 10
	DefaultOpenDuration = 30 * time.Second
	DefaultHalfOpenMax  = 3
)

// Option 是熔断器参数。
type Option func(*options)

type options struct {
	failureRatio float64
	request      int64
	window       time.Duration
	bucket       int
	openDuration time.Duration
	halfOpenMax  int64
}

// WithFailureRatio 设置失败率阈值。ratio 取值 (0,1)，越接近 1 越晚熔断。
func WithFailureRatio(ratio float64) Option {
	return func(o *options) { o.failureRatio = ratio }
}

// WithRequest 设置窗口内触发评估的最小请求数。n 是最小请求数。
func WithRequest(n int64) Option {
	return func(o *options) { o.request = n }
}

// WithWindow 设置滚动统计窗口。
func WithWindow(d time.Duration) Option {
	return func(o *options) { o.window = d }
}

// WithBucket 设置窗口分桶数。n 是分桶数。
func WithBucket(n int) Option {
	return func(o *options) { o.bucket = n }
}

// WithOpenDuration 设置熔断打开后的冷却时长。d 是冷却时长。
func WithOpenDuration(d time.Duration) Option {
	return func(o *options) { o.openDuration = d }
}

// WithHalfOpenMax 设置半开状态下允许的试探请求数。
func WithHalfOpenMax(n int64) Option {
	return func(o *options) { o.halfOpenMax = n }
}

// Client 返回 gRPC 客户端熔断中间件。opts 是熔断参数。
// 放在客户端中间件链的最内层；只有 5xx 类错误算失败。
func Client(opts ...Option) middleware.Middleware {
	o := &options{
		failureRatio: DefaultFailureRatio,
		request:      DefaultRequest,
		window:       DefaultWindow,
		bucket:       DefaultBucket,
		openDuration: DefaultOpenDuration,
		halfOpenMax:  DefaultHalfOpenMax,
	}
	for _, opt := range opts {
		opt(o)
	}
	if o.failureRatio <= 0 || o.failureRatio >= 1 {
		o.failureRatio = DefaultFailureRatio
	}
	if o.request < 1 {
		o.request = DefaultRequest
	}
	if o.window <= 0 {
		o.window = DefaultWindow
	}
	if o.bucket < 1 {
		o.bucket = DefaultBucket
	}
	if o.openDuration <= 0 {
		o.openDuration = DefaultOpenDuration
	}
	if o.halfOpenMax < 1 {
		o.halfOpenMax = DefaultHalfOpenMax
	}

	return circuitbreaker.Client(
		circuitbreaker.WithBreakerFactory(func() circuitbreaker.CircuitBreaker {
			return newBreaker(o)
		}),
	)
}

// 熔断器状态。
const (
	stateClosed int32 = iota
	stateOpen
	stateHalfOpen
)

// breaker 三态熔断器：closed → open → halfOpen → closed。
// 实现 Kratos 的 circuitbreaker.CircuitBreaker 接口。
type breaker struct {
	mu sync.Mutex

	state int32

	failureRatio float64
	minRequests  int64
	openDuration time.Duration
	halfOpenMax  int64

	counter *rollingCounter

	openedAt         time.Time
	halfOpenInFlight int64
	halfOpenFailed   int64

	// pendingLocalReject 标记：最近一次 Allow() 是否返回了本地拒绝。
	// 用于让紧随其后的 MarkFailed() 识别"这次失败是本地拒绝，不是真实调用失败"。
	// 由 b.mu 保护。
	pendingLocalReject bool
}

func newBreaker(o *options) *breaker {
	return &breaker{
		state:        stateClosed,
		failureRatio: o.failureRatio,
		minRequests:  o.request,
		openDuration: o.openDuration,
		halfOpenMax:  o.halfOpenMax,
		counter:      newRollingCounter(o.bucket, o.window/time.Duration(o.bucket)),
	}
}

// Allow 报告请求能否通过。被熔断拒掉时返回 ErrNotAllowed。
func (b *breaker) Allow() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	// 每次 Allow 都先重置标记，保证语义是"最近一次 Allow 的结果"。
	b.pendingLocalReject = false

	switch b.state {
	case stateOpen:
		if time.Since(b.openedAt) < b.openDuration {
			b.pendingLocalReject = true // ← 本地拒绝，打标记
			return ErrNotAllowed
		}
		// 冷却期过了 → 半开，放一个试探请求看看下游恢复没有
		b.state = stateHalfOpen
		b.halfOpenInFlight = 1
		b.halfOpenFailed = 0
		return nil
	case stateHalfOpen:
		if b.halfOpenInFlight >= b.halfOpenMax {
			b.pendingLocalReject = true // ← 本地拒绝（名额满），打标记
			return ErrNotAllowed
		}
		b.halfOpenInFlight++
		return nil
	default:
		return nil
	}
}

// MarkSuccess 记一次成功。
func (b *breaker) MarkSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()

	// 防御性：清理标记，避免残留
	b.pendingLocalReject = false

	switch b.state {
	case stateHalfOpen:
		// 试探请求成功 → 认为下游恢复了
		b.halfOpenFailed = 0
		if b.halfOpenInFlight <= 1 {
			b.state = stateClosed
			b.counter.reset()
		}
		b.halfOpenInFlight--
		return
	default:
		b.counter.add(true)
	}
}

// MarkFailed 记一次失败。
func (b *breaker) MarkFailed() {
	b.mu.Lock()
	defer b.mu.Unlock()

	// 核心修复：如果这次 MarkFailed 是本地拒绝触发的，直接丢弃，不做任何变更。
	// 本地拒绝不代表下游状态，绝不能污染熔断判断。
	if b.pendingLocalReject {
		b.pendingLocalReject = false
		return
	}

	switch b.state {
	case stateHalfOpen:
		// 试探请求又失败 → 重新熔断，冷却期重新开始
		b.halfOpenFailed++
		b.halfOpenInFlight--
		// 探针真实失败 → 立刻重开，冷却期重置。
		b.state = stateOpen
		b.openedAt = time.Now()
		return
	case stateOpen:
		return
	default:
		b.counter.add(false)
		// summary 返回 (成功数, 总数) —— 失败数要自己减出来。
		successes, total := b.counter.summary()
		if total < b.minRequests {
			return
		}
		failures := total - successes
		if float64(failures)/float64(total) > b.failureRatio {
			b.state = stateOpen
			b.openedAt = time.Now()
		}
	}
}

// rollingCounter 滚动窗口计数器。
type rollingCounter struct {
	mu       sync.Mutex
	buckets  []countBucket
	duration time.Duration
}

type countBucket struct {
	slot    int64
	success int64
	failure int64
}

// newRollingCounter 造一个滚动窗口计数器。
// size 分桶数
// duration 单桶时长
func newRollingCounter(size int, duration time.Duration) *rollingCounter {
	if size < 1 {
		size = 1
	}
	if duration <= 0 {
		duration = time.Second
	}
	return &rollingCounter{
		buckets:  make([]countBucket, size),
		duration: duration,
	}
}

func (c *rollingCounter) currentSlot() int64 {
	return time.Now().UnixNano() / int64(c.duration)
}

// add 记一次调用。success 是这次调用是否成功。
func (c *rollingCounter) add(success bool) {
	slot := c.currentSlot()
	idx := slot % int64(len(c.buckets))

	c.mu.Lock()
	defer c.mu.Unlock()
	bucket := &c.buckets[idx]
	if bucket.slot != slot {
		bucket.slot = slot
		bucket.success = 0
		bucket.failure = 0
	}
	if success {
		bucket.success++
	} else {
		bucket.failure++
	}
}

// summary 返回窗口内的成功数和总数。
func (c *rollingCounter) summary() (successes, total int64) {
	slot := c.currentSlot()
	size := int64(len(c.buckets))

	c.mu.Lock()
	defer c.mu.Unlock()
	for _, bucket := range c.buckets {
		if bucket.slot == 0 || slot-bucket.slot >= size || bucket.slot > slot {
			continue
		}
		successes += bucket.success
		total += bucket.success + bucket.failure
	}
	return successes, total
}

// reset 清空计数。
func (c *rollingCounter) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.buckets {
		c.buckets[i] = countBucket{}
	}
}
