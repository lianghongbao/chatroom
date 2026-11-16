// Package breaker 提供跨服务 gRPC 调用的熔断中间件。
//
// 按 operation 粒度统计 —— 一个端点挂了不影响同一个下游的其它端点。
//
// 状态机用 sony/gobreaker v2，不自己实现。它按 generation 号丢弃迟到的
// 结果，按 age 号把结果写回请求发起时的那个桶，open 态直接返错不动计数。
//
// 语义：
//
//   - 样本不够不熔断（Requests 少于阈值时 ReadyToTrip 返回 false）。
//   - 半开态放行 MaxRequests 个探针，连续成功 MaxRequests 次才关闭。
//   - 设了 BucketPeriod 就是滚动窗口：Interval 切成 N 个桶，统计逐桶滑出，
//     判定看的是「最近 Interval 这段时间」。BucketPeriod <= 0 是固定窗口。
package breaker

import (
	"context"
	"errors"
	"sync"
	"time"

	kratoserrors "github.com/go-kratos/kratos/v3/errors"
	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/transport"
	"github.com/sony/gobreaker/v2"
)

// ErrNotAllowed 熔断打开，请求被拒。调用方按 errors.Is 判它。
var ErrNotAllowed = kratoserrors.ServiceUnavailable("CIRCUITBREAKER", "circuit breaker is open")

// 默认参数。
const (
	// DefaultFailureRatio 触发熔断的失败率阈值。
	DefaultFailureRatio = 0.5
	// DefaultRequest 统计周期内触发评估的最小请求数。
	DefaultRequest = 20
	// DefaultInterval 统计周期。周期结束后清空计数。
	DefaultInterval = 10 * time.Second
	// DefaultBucketPeriod 滚动窗口的桶宽。滚动窗口就是靠它切出来的。
	DefaultBucketPeriod = time.Second
	// DefaultTimeout 熔断打开后多久进半开状态试探。
	DefaultTimeout = 30 * time.Second
	// DefaultMaxRequests 半开状态下允许的试探请求数，同时也是关闭所需的连续成功数。
	DefaultMaxRequests = 1
)

// Option 是熔断器参数。
type Option func(*options)

type options struct {
	failureRatio float64
	request      uint32
	interval     time.Duration
	bucketPeriod time.Duration
	timeout      time.Duration
	maxRequests  uint32
}

// WithFailureRatio 设置失败率阈值。ratio 取值 (0,1)，越接近 1 越晚熔断。
func WithFailureRatio(ratio float64) Option {
	return func(o *options) { o.failureRatio = ratio }
}

// WithRequest 设置周期内触发评估的最小请求数。n 是最小请求数。
func WithRequest(n int64) Option {
	return func(o *options) {
		if n > 0 {
			o.request = uint32(n)
		}
	}
}

// WithInterval 设置统计周期。周期结束后清空计数。
func WithInterval(d time.Duration) Option {
	return func(o *options) { o.interval = d }
}

// WithBucketPeriod 设置滚动窗口的桶宽。d 是桶宽。
//
// d <= 0 表示固定窗口（整个 Interval 就是一个桶）。
//
// d 必须整除 Interval —— v2 会把 Interval 向上取整到桶宽的整数倍。
func WithBucketPeriod(d time.Duration) Option {
	return func(o *options) { o.bucketPeriod = d }
}

// WithTimeout 设置熔断打开后的冷却时长。d 是冷却时长。
func WithTimeout(d time.Duration) Option {
	return func(o *options) { o.timeout = d }
}

// WithMaxRequests 设置半开状态下允许的试探请求数，同时也是关闭所需的连续成功数。
func WithMaxRequests(n int64) Option {
	return func(o *options) {
		if n > 0 {
			o.maxRequests = uint32(n)
		}
	}
}

// Client 返回 gRPC 客户端熔断中间件。opts 是熔断参数。
//
// 放在客户端中间件链的最内层；只有 5xx 类错误算失败。
//
// 不用 Kratos 的 circuitbreaker.Client()：它的 CircuitBreaker 接口是
// Allow() / MarkSuccess() / MarkFailed() 三段式，中间隔着一次真实 RPC 调用，
// 并发下无法把「这次结果」和「这次请求」对应起来。这里用 gobreaker 的
// Execute() 把整次调用包住，对应关系由它内部按 generation 管理。
func Client(opts ...Option) middleware.Middleware {
	o := &options{
		failureRatio: DefaultFailureRatio,
		request:      DefaultRequest,
		interval:     DefaultInterval,
		bucketPeriod: DefaultBucketPeriod,
		timeout:      DefaultTimeout,
		maxRequests:  DefaultMaxRequests,
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
	if o.interval <= 0 {
		o.interval = DefaultInterval
	}
	if o.bucketPeriod < 0 {
		o.bucketPeriod = 0
	}
	if o.timeout <= 0 {
		o.timeout = DefaultTimeout
	}
	if o.maxRequests < 1 {
		o.maxRequests = DefaultMaxRequests
	}

	// 每个 operation 一个熔断器。
	group := newBreakerGroup(o)

	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			info, _ := transport.FromClientContext(ctx)
			cb := group.Get(info.Operation())

			reply, err := cb.Execute(func() (any, error) {
				return handler(ctx, req)
			})
			if err != nil {
				// 熔断拒掉的请求翻译成 503 —— 调用方看到的是「下游不可用」。
				if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
					return nil, ErrNotAllowed
				}
				return nil, err
			}
			return reply, nil
		}
	}
}

// breakerGroup 按 operation 懒加载熔断器。
type breakerGroup struct {
	o *options

	mu       sync.Mutex
	breakers map[string]*gobreaker.CircuitBreaker[any]
}

func newBreakerGroup(o *options) *breakerGroup {
	return &breakerGroup{o: o, breakers: make(map[string]*gobreaker.CircuitBreaker[any])}
}

// Get 取某个 operation 的熔断器，没有就造一个。key 是 operation 名。
func (g *breakerGroup) Get(key string) *gobreaker.CircuitBreaker[any] {
	g.mu.Lock()
	defer g.mu.Unlock()
	if cb, ok := g.breakers[key]; ok {
		return cb
	}
	cb := gobreaker.NewCircuitBreaker[any](gobreaker.Settings{
		Name:         key,
		MaxRequests:  g.o.maxRequests,
		Interval:     g.o.interval,
		BucketPeriod: g.o.bucketPeriod,
		Timeout:      g.o.timeout,
		ReadyToTrip:  readyToTrip(g.o.request, g.o.failureRatio),
		IsSuccessful: func(err error) bool {
			return isSuccessful(err)
		},
		IsExcluded: isExcluded,
	})
	g.breakers[key] = cb
	return cb
}

// readyToTrip 判定该不该熔断。
//
// minRequests 是周期内触发评估的最小请求数；ratio 是失败率阈值。
//
// 样本不够时返回 false —— 流量低的时候三五次失败说明不了问题。
func readyToTrip(minRequests uint32, ratio float64) func(counts gobreaker.Counts) bool {
	return func(counts gobreaker.Counts) bool {
		if counts.Requests < minRequests {
			return false
		}
		total := counts.TotalSuccesses + counts.TotalFailures
		if total == 0 {
			return false
		}
		return float64(counts.TotalFailures)/float64(total) > ratio
	}
}

// isSuccessful 判断这次调用算不算成功。
//
// 只有 5xx（500 / 503 / 504）算失败。404「用户不存在」是业务结果，
// 不是下游不健康 —— 把它算成失败会让熔断器误判下游挂了。
//
// err 是这次调用返回的错误，nil 表示成功。
func isSuccessful(err error) bool {
	if err == nil {
		return true
	}
	// gRPC 错误是 status.Error，没有 HTTP 状态码，只能按错误类型判定。
	if kratoserrors.IsInternalServer(err) || kratoserrors.IsServiceUnavailable(err) || kratoserrors.IsGatewayTimeout(err) {
		return false
	}
	return true
}

// isExcluded 判断这次调用该不该被完全排除在统计之外。
//
// 调用方自己把请求取消了（客户端断连、超时取消），跟下游健不健康没关系 ——
// 把它算成失败会把好端点误判成挂了。这里既不算成功也不算失败。
//
// err 是这次调用返回的错误。
func isExcluded(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
