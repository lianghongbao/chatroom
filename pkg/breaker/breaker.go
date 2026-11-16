// Package breaker 提供跨服务 gRPC 调用的熔断中间件。
//
// 按 operation 粒度统计 —— 一个端点挂了不影响同一个下游的其它端点。
// 状态机用 sony/gobreaker v2。
//
// 语义：
//
//   - 只有 5xx（500 / 503 / 504）算失败。
//   - 样本不够不熔断。
//   - 半开态放行 MaxRequests 个探针，连续成功 MaxRequests 次才关闭。
//   - 统计看最近 Interval 这段时间（滚动窗口）。
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
	// DefaultInterval 统计周期长度。
	DefaultInterval = 10 * time.Second
	// DefaultBucketPeriod 滚动窗口的桶宽。滚动窗口就是靠它切出来的。
	//
	// 只在没传 Settings 时生效 —— 传了 Settings 就以调用方的为准。
	DefaultBucketPeriod = time.Second
	// DefaultTimeout 熔断打开后多久进半开状态试探。
	DefaultTimeout = 30 * time.Second
	// DefaultMaxRequests 半开状态下允许的试探请求数，同时也是关闭所需的连续成功数。
	DefaultMaxRequests = 1
)

// Client 返回 gRPC 客户端熔断中间件。
//
// 放在客户端中间件链的最内层。st 是熔断参数，留空用默认值。
//
// 按 operation 粒度各有一个熔断器 —— 一个端点挂了不影响同一个下游的
// 其它端点。
func Client(st ...gobreaker.Settings) middleware.Middleware {
	group := newBreakerGroup(st)

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
	settings gobreaker.Settings

	mu       sync.Mutex
	breakers map[string]*gobreaker.CircuitBreaker[any]
}

func newBreakerGroup(st []gobreaker.Settings) *breakerGroup {
	// 没传 Settings 就用默认那份；传了就以调用方的为准。
	settings := gobreaker.Settings{
		MaxRequests:  DefaultMaxRequests,
		Interval:     DefaultInterval,
		BucketPeriod: DefaultBucketPeriod,
		Timeout:      DefaultTimeout,
		ReadyToTrip:  readyToTrip(DefaultRequest, DefaultFailureRatio),
	}
	if len(st) > 0 {
		settings = st[0]
	}

	// 缺省的字段走默认值。BucketPeriod 不补 —— gobreaker 把 <= 0 当作
	// 固定窗口，调用方想要固定窗口时传的就是 0。
	if settings.MaxRequests == 0 {
		settings.MaxRequests = DefaultMaxRequests
	}
	if settings.Interval <= 0 {
		settings.Interval = DefaultInterval
	}
	if settings.Timeout <= 0 {
		settings.Timeout = DefaultTimeout
	}
	if settings.ReadyToTrip == nil {
		settings.ReadyToTrip = readyToTrip(DefaultRequest, DefaultFailureRatio)
	}
	// IsSuccessful / IsExcluded 是这个包对外的约定，不让调用方改 ——
	// 只有 5xx 算失败、取消的请求排除在外。
	settings.IsSuccessful = isSuccessful
	settings.IsExcluded = isExcluded

	return &breakerGroup{settings: settings, breakers: make(map[string]*gobreaker.CircuitBreaker[any])}
}

// Get 取某个 operation 的熔断器，没有就造一个。key 是 operation 名。
func (g *breakerGroup) Get(key string) *gobreaker.CircuitBreaker[any] {
	g.mu.Lock()
	defer g.mu.Unlock()
	if cb, ok := g.breakers[key]; ok {
		return cb
	}
	settings := g.settings
	settings.Name = key
	cb := gobreaker.NewCircuitBreaker[any](settings)
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
