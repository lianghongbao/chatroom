// Package breaker 提供跨服务 gRPC 调用的熔断中间件。
//
// 按 operation 粒度统计（Kratos 内部按 gRPC method 分组）—— 一个端点挂了
// 不影响同一个下游的其它端点。
//
// 状态机用 sony/gobreaker，不自己实现：
//
//   - **generation 号**解决「迟到的结果污染新一代计数」。每次状态切换
//     generation++，旧请求的结果回来时对不上号，天然被丢弃。自研实现里
//     只能靠 breaker 上的单个 bool 标记来猜「这个结果属于哪一代请求」——
//     并发下必然串。
//   - **open 态直接返错**，不动计数。本地拒绝不代表下游状态，绝不能污染
//     熔断判断。
//
// 语义说明（跟 gobreaker 原生语义一致，不是 SRE 滚动窗口）：
//
//   - closed 态每 **Interval** 个周期清空一次计数。判定用的是「上一个周期」
//     的统计，不是「回看 N 秒」。
//   - 样本不够不熔断：Requests 少于阈值时 ReadyToTrip 返回 false。
//   - 半开态放行 MaxRequests 个探针，连续成功 MaxRequests 次才关闭。
package breaker

import (
	"context"
	"errors"
	"sync"
	"time"

	kratoserrors "github.com/go-kratos/kratos/v3/errors"
	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/transport"
	"github.com/sony/gobreaker"
)

// ErrNotAllowed 熔断打开，请求被拒。
//
// 保留这个名字是为了跟之前的实现对齐 —— 调用方按 errors.Is 判它。
// 中间件会把 gobreaker 的 ErrOpenState / ErrTooManyRequests 翻译成它。
var ErrNotAllowed = kratoserrors.ServiceUnavailable("CIRCUITBREAKER", "circuit breaker is open")

// 默认参数。
const (
	// DefaultFailureRatio 触发熔断的失败率阈值。
	DefaultFailureRatio = 0.5
	// DefaultRequest 统计周期内触发评估的最小请求数。
	DefaultRequest = 20
	// DefaultInterval 统计周期。周期结束后清空计数。
	DefaultInterval = 10 * time.Second
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
// 并发下无法把「这次结果」和「这次请求」对应起来 —— 而这正是熔断器最需要
// 准确的信息。这里直接用 gobreaker 的 Execute() 把整次调用包住，
// 请求与结果的对应关系由 gobreaker 内部按 generation 管理。
func Client(opts ...Option) middleware.Middleware {
	o := &options{
		failureRatio: DefaultFailureRatio,
		request:      DefaultRequest,
		interval:     DefaultInterval,
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
	if o.timeout <= 0 {
		o.timeout = DefaultTimeout
	}
	if o.maxRequests < 1 {
		o.maxRequests = DefaultMaxRequests
	}

	// 每个 operation 一个熔断器 —— 一个端点挂了不影响同下游的其它端点。
	// group 是 Kratos 内部的按 key 懒加载容器。
	group := newBreakerGroup(o)

	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			info, _ := transport.FromClientContext(ctx)
			cb := group.Get(info.Operation())

			reply, err := cb.Execute(func() (interface{}, error) {
				return handler(ctx, req)
			})
			if err != nil {
				// 熔断拒掉的请求翻译成 503 —— 调用方看到的是「下游不可用」，
				// 而不是 gobreaker 的内部错误串。
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
	breakers map[string]*gobreaker.CircuitBreaker
}

func newBreakerGroup(o *options) *breakerGroup {
	return &breakerGroup{o: o, breakers: make(map[string]*gobreaker.CircuitBreaker)}
}

// Get 取某个 operation 的熔断器，没有就造一个。key 是 operation 名。
func (g *breakerGroup) Get(key string) *gobreaker.CircuitBreaker {
	g.mu.Lock()
	defer g.mu.Unlock()
	if cb, ok := g.breakers[key]; ok {
		return cb
	}
	cb := gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        key,
		MaxRequests: g.o.maxRequests,
		Interval:    g.o.interval,
		Timeout:     g.o.timeout,
		ReadyToTrip: readyToTrip(g.o.request, g.o.failureRatio),
		IsSuccessful: func(err error) bool {
			return isSuccessful(err)
		},
	})
	g.breakers[key] = cb
	return cb
}

// readyToTrip 判定该不该熔断。
//
// minRequests 是周期内触发评估的最小请求数；ratio 是失败率阈值。
//
// 样本不够时返回 false —— 流量低的时候三五次失败说明不了问题。
// 这跟 Kratos 内置的 SRE 熔断器不一样：内置的窗口只有 3s，且要求窗口内至少
// 20 个请求才评估，5 QPS 的服务窗口内才 15 个请求，永远达不到 20 ——
// 熔断器等于没有。
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
	// 必须用 errors.IsInternalServer 这类判定，不能看 HTTP 状态码 ——
	// gRPC 错误是 status.Error，没有 HTTP 状态码。
	if kratoserrors.IsInternalServer(err) || kratoserrors.IsServiceUnavailable(err) || kratoserrors.IsGatewayTimeout(err) {
		return false
	}
	return true
}
