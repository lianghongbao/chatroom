package breaker

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kratoserrors "github.com/go-kratos/kratos/v3/errors"
	"github.com/go-kratos/kratos/v3/transport"
	"github.com/sony/gobreaker/v2"
)

// fakeTransport 是 transport.Transporter 的最小实现。
type fakeTransport struct {
	operation string
}

func (f *fakeTransport) Kind() transport.Kind            { return transport.KindGRPC }
func (f *fakeTransport) Endpoint() string                { return "discovery:///test" }
func (f *fakeTransport) Operation() string               { return f.operation }
func (f *fakeTransport) RequestHeader() transport.Header { return nil }
func (f *fakeTransport) ReplyHeader() transport.Header   { return nil }

// testSettings 造一份测试用的熔断参数。
func testSettings(minRequests uint32, ratio float64, timeout time.Duration) gobreaker.Settings {
	return gobreaker.Settings{
		MaxRequests:  1,
		Interval:     10 * time.Second,
		BucketPeriod: time.Second,
		Timeout:      timeout,
		ReadyToTrip:  readyToTrip(minRequests, ratio),
	}
}

// 失败率超阈值且样本够 → 熔断打开。
func TestBreakerOpensAfterFailureRatio(t *testing.T) {
	g := newBreakerGroup([]gobreaker.Settings{testSettings(4, 0.5, time.Minute)})
	cb := g.Get("/svc/X")

	for i := 0; i < 4; i++ {
		_, err := cb.Execute(func() (any, error) { return nil, errors.New("boom") })
		if err == nil {
			t.Fatal("下游失败时应透传错误")
		}
	}

	if cb.State() != gobreaker.StateOpen {
		t.Fatalf("失败率超阈值后应熔断, state=%v", cb.State())
	}
}

// 样本不够时不该熔断 —— 流量低的时候三五次失败说明不了问题。
func TestBreakerStaysClosedBelowMinRequests(t *testing.T) {
	g := newBreakerGroup([]gobreaker.Settings{testSettings(4, 0.5, time.Minute)})
	cb := g.Get("/svc/X")

	for i := 0; i < 3; i++ {
		cb.Execute(func() (any, error) { return nil, errors.New("boom") })
	}

	if cb.State() != gobreaker.StateClosed {
		t.Fatalf("样本不足时应保持闭合, state=%v", cb.State())
	}
}

// 成功率够高就不该熔断 —— 偶发失败不算下游挂了。
func TestBreakerStaysClosedWithGoodSuccessRatio(t *testing.T) {
	g := newBreakerGroup([]gobreaker.Settings{testSettings(4, 0.5, time.Minute)})
	cb := g.Get("/svc/X")

	// 10 次里只失败 1 次（失败率 10%），远低于 50% 的阈值
	for i := 0; i < 10; i++ {
		if i == 0 {
			cb.Execute(func() (any, error) { return nil, errors.New("boom") })
		} else {
			cb.Execute(func() (any, error) { return "ok", nil })
		}
	}

	if cb.State() != gobreaker.StateClosed {
		t.Fatalf("失败率 10%% 不该触发熔断, state=%v", cb.State())
	}
}

// 冷却期过了要进半开。
func TestBreakerHalfOpensAfterCooldown(t *testing.T) {
	g := newBreakerGroup([]gobreaker.Settings{testSettings(2, 0.5, 50*time.Millisecond)})
	cb := g.Get("/svc/X")

	for i := 0; i < 2; i++ {
		cb.Execute(func() (any, error) { return nil, errors.New("boom") })
	}
	if cb.State() != gobreaker.StateOpen {
		t.Fatalf("应处于熔断状态, state=%v", cb.State())
	}

	time.Sleep(80 * time.Millisecond)

	if cb.State() != gobreaker.StateHalfOpen {
		t.Fatalf("冷却期后应进半开, state=%v", cb.State())
	}
}

// 半开的试探成功 → 恢复关闭。
func TestBreakerClosesAfterSuccessfulProbe(t *testing.T) {
	g := newBreakerGroup([]gobreaker.Settings{testSettings(2, 0.5, 50*time.Millisecond)})
	cb := g.Get("/svc/X")

	for i := 0; i < 2; i++ {
		cb.Execute(func() (any, error) { return nil, errors.New("boom") })
	}
	time.Sleep(80 * time.Millisecond)

	if cb.State() != gobreaker.StateHalfOpen {
		t.Fatalf("冷却期后应进半开, state=%v", cb.State())
	}
	cb.Execute(func() (any, error) { return "ok", nil })

	if cb.State() != gobreaker.StateClosed {
		t.Fatalf("试探成功后应恢复关闭, state=%v", cb.State())
	}
}

// 半开的试探又失败 → 重新熔断，冷却期重新开始。
func TestBreakerReopensAfterFailedProbe(t *testing.T) {
	g := newBreakerGroup([]gobreaker.Settings{testSettings(2, 0.5, 50*time.Millisecond)})
	cb := g.Get("/svc/X")

	for i := 0; i < 2; i++ {
		cb.Execute(func() (any, error) { return nil, errors.New("boom") })
	}
	time.Sleep(80 * time.Millisecond)

	if cb.State() != gobreaker.StateHalfOpen {
		t.Fatalf("冷却期后应进半开, state=%v", cb.State())
	}
	cb.Execute(func() (any, error) { return nil, errors.New("boom") })

	if cb.State() != gobreaker.StateOpen {
		t.Fatalf("试探失败后应重新熔断, state=%v", cb.State())
	}
}

// 熔断打开期间的本地拒绝不该污染计数 —— 本地拒绝不代表下游状态。
func TestLocalRejectsDoNotPolluteCounts(t *testing.T) {
	g := newBreakerGroup([]gobreaker.Settings{testSettings(2, 0.5, time.Minute)})
	cb := g.Get("/svc/X")

	for i := 0; i < 2; i++ {
		cb.Execute(func() (any, error) { return nil, errors.New("boom") })
	}
	if cb.State() != gobreaker.StateOpen {
		t.Fatalf("应熔断, state=%v", cb.State())
	}

	before := cb.Counts()

	// 熔断期间连拒 50 次 —— 下游根本没被调用
	for i := 0; i < 50; i++ {
		_, err := cb.Execute(func() (any, error) {
			t.Error("熔断期间不该调到下游")
			return nil, nil
		})
		if err == nil {
			t.Fatal("熔断期间应拒绝")
		}
	}

	after := cb.Counts()
	if after.Requests != before.Requests {
		t.Errorf("本地拒绝污染了计数: Requests %d → %d", before.Requests, after.Requests)
	}
	if after.TotalFailures != before.TotalFailures {
		t.Errorf("本地拒绝被记成了失败: TotalFailures %d → %d", before.TotalFailures, after.TotalFailures)
	}
}

// 并发下本地拒绝仍然不污染计数。
func TestConcurrentLocalRejectsDoNotPolluteCounts(t *testing.T) {
	st := testSettings(1000, 0.99, time.Hour)
	st.Interval = time.Hour
	g := newBreakerGroup([]gobreaker.Settings{st})
	cb := g.Get("/svc/X")

	// 先让它熔断
	for i := 0; i < 1000; i++ {
		cb.Execute(func() (any, error) { return nil, errors.New("boom") })
	}
	if cb.State() != gobreaker.StateOpen {
		t.Fatalf("应熔断, state=%v", cb.State())
	}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				cb.Execute(func() (any, error) { return nil, nil })
			}
		}()
	}
	wg.Wait()

	counts := cb.Counts()
	if counts.Requests != 0 || counts.TotalFailures != 0 {
		t.Errorf("并发下本地拒绝污染了计数: %+v", counts)
	}
}

// 迟到的结果不该污染新一代的计数 —— 对不上 generation 就丢弃。
func TestStaleResultDoesNotPolluteNewGeneration(t *testing.T) {
	g := newBreakerGroup([]gobreaker.Settings{testSettings(2, 0.5, 50*time.Millisecond)})
	cb := g.Get("/svc/X")

	for i := 0; i < 2; i++ {
		cb.Execute(func() (any, error) { return nil, errors.New("boom") })
	}
	time.Sleep(80 * time.Millisecond)

	// 半开态放探针，成功后切回 closed —— 计数应该是新一代的（干净的）
	if cb.State() != gobreaker.StateHalfOpen {
		t.Fatalf("应进半开, state=%v", cb.State())
	}
	cb.Execute(func() (any, error) { return "ok", nil })
	if cb.State() != gobreaker.StateClosed {
		t.Fatalf("试探成功后应关闭, state=%v", cb.State())
	}
	if cb.Counts().TotalFailures != 0 {
		t.Errorf("新一代的计数应清零, got %+v", cb.Counts())
	}
}

// 统计周期到了之后计数清空 —— 固定窗口（bucketPeriod=0）下清零重算。
func TestIntervalClearsCounts(t *testing.T) {
	st := testSettings(100, 0.5, time.Minute)
	st.Interval = 100 * time.Millisecond
	st.BucketPeriod = 0 // 固定窗口
	g := newBreakerGroup([]gobreaker.Settings{st})
	cb := g.Get("/svc/X")

	for i := 0; i < 5; i++ {
		cb.Execute(func() (any, error) { return nil, errors.New("boom") })
	}
	if cb.Counts().Requests != 5 {
		t.Fatalf("应记了 5 次, got %+v", cb.Counts())
	}

	// 等过周期，再来一个请求触发惰性清空
	time.Sleep(150 * time.Millisecond)
	cb.Execute(func() (any, error) { return nil, errors.New("boom") })

	if cb.Counts().Requests != 1 {
		t.Errorf("周期过期后计数应清空重算, got %+v", cb.Counts())
	}
}

// 桶宽必须整除周期 —— v2 会把 Interval 向上取整到 BucketPeriod 的整数倍。
func TestBucketPeriodRoundsIntervalUp(t *testing.T) {
	st := testSettings(100, 0.5, time.Minute)
	st.Interval = 100 * time.Millisecond
	st.BucketPeriod = time.Second
	g := newBreakerGroup([]gobreaker.Settings{st})
	cb := g.Get("/svc/X")

	for i := 0; i < 5; i++ {
		cb.Execute(func() (any, error) { return nil, errors.New("boom") })
	}

	// 原始 Interval（100ms）过了，但窗口被撑到 1s，计数不该清
	time.Sleep(150 * time.Millisecond)
	cb.Execute(func() (any, error) { return nil, errors.New("boom") })
	if got := cb.Counts().Requests; got != 6 {
		t.Errorf("窗口被撑到 1s，150ms 时不该清空, got %+v", cb.Counts())
	}

	// 撑过的窗口真到期了才清
	time.Sleep(1100 * time.Millisecond)
	cb.Execute(func() (any, error) { return nil, errors.New("boom") })
	if got := cb.Counts().Requests; got != 1 {
		t.Errorf("1.25s 后窗口应清空, got %+v", cb.Counts())
	}
}

// v2 的滚动窗口：桶宽小于周期时，统计随时间逐桶滑出，
// 而不是整段清零重算。窗口里残留的旧请求会被自然挤出去。
func TestRollingWindowSlidesOutOldBuckets(t *testing.T) {
	st := testSettings(100, 0.5, time.Minute)
	st.Interval = 200 * time.Millisecond
	st.BucketPeriod = 50 * time.Millisecond
	g := newBreakerGroup([]gobreaker.Settings{st})
	cb := g.Get("/svc/X")

	// 先攒 4 次失败
	for i := 0; i < 4; i++ {
		cb.Execute(func() (any, error) { return nil, errors.New("boom") })
	}
	if cb.Counts().Requests != 4 {
		t.Fatalf("应记了 4 次, got %+v", cb.Counts())
	}

	// 等过整个窗口 —— 4 个桶依次滑出，计数应该归零
	time.Sleep(250 * time.Millisecond)
	// 来一个请求触发惰性滚动
	cb.Execute(func() (any, error) { return nil, errors.New("boom") })

	if got := cb.Counts().Requests; got != 1 {
		t.Errorf("窗口滑过后只剩这次的 1 次, got %+v", cb.Counts())
	}
}

// 排除项：调用方自己取消的请求不该进统计 ——
// 它跟下游健不健康没关系，算成失败会把好端点误判成挂了。
func TestExcludedErrorsDoNotAffectCounts(t *testing.T) {
	g := newBreakerGroup([]gobreaker.Settings{testSettings(4, 0.5, time.Minute)})
	cb := g.Get("/svc/X")

	for i := 0; i < 20; i++ {
		cb.Execute(func() (any, error) { return nil, context.Canceled })
	}

	counts := cb.Counts()
	if counts.TotalFailures != 0 {
		t.Errorf("取消的请求不该算失败, got %+v", counts)
	}
	if counts.TotalSuccesses != 0 {
		t.Errorf("取消的请求不该算成功, got %+v", counts)
	}
	if counts.TotalExclusions != 20 {
		t.Errorf("取消的请求应记成排除项, got %+v", counts)
	}
	// 排除项不算有效样本，熔断器不该被它们撑开
	if cb.State() != gobreaker.StateClosed {
		t.Errorf("全是取消请求时不该熔断, state=%v", cb.State())
	}
}

// 超时取消同样排除在外。
func TestDeadlineExceededIsExcluded(t *testing.T) {
	g := newBreakerGroup([]gobreaker.Settings{testSettings(4, 0.5, time.Minute)})
	cb := g.Get("/svc/X")

	for i := 0; i < 10; i++ {
		cb.Execute(func() (any, error) { return nil, context.DeadlineExceeded })
	}

	counts := cb.Counts()
	if counts.TotalFailures != 0 || counts.TotalSuccesses != 0 {
		t.Errorf("超时取消不该计入成败, got %+v", counts)
	}
	if counts.TotalExclusions != 10 {
		t.Errorf("应记成排除项, got %+v", counts)
	}
}

// 失败率判定是自己写的 ReadyToTrip，阈值和样本数按 readyToTrip 的参数走。
func TestReadyToTripThreshold(t *testing.T) {
	// 样本不够不熔断
	g := newBreakerGroup([]gobreaker.Settings{testSettings(20, 0.5, time.Minute)})
	cb := g.Get("/svc/X")
	for i := 0; i < 19; i++ {
		cb.Execute(func() (any, error) { return nil, errors.New("boom") })
	}
	if cb.State() != gobreaker.StateClosed {
		t.Fatalf("19 次失败不该熔断（样本不足 20）, state=%v", cb.State())
	}
	cb.Execute(func() (any, error) { return nil, errors.New("boom") })
	if cb.State() != gobreaker.StateOpen {
		t.Fatalf("20 次失败该熔断, state=%v", cb.State())
	}
}

// 调用方传自己的 ReadyToTrip 就能改判定逻辑。
func TestCallerSuppliedReadyToTrip(t *testing.T) {
	st := testSettings(2, 0.9, time.Minute)
	g := newBreakerGroup([]gobreaker.Settings{st})
	cb := g.Get("/svc/Y")

	for i := 0; i < 2; i++ {
		cb.Execute(func() (any, error) { return nil, errors.New("boom") })
	}
	if cb.State() != gobreaker.StateOpen {
		t.Fatalf("自定义阈值应生效, state=%v", cb.State())
	}
}

// 显式传 Settings 时，BucketPeriod=0 就是固定窗口 —— 不能被当成「没填」
// 而替换成默认值。
func TestExplicitZeroBucketPeriodKeepsFixedWindow(t *testing.T) {
	st := testSettings(100, 0.5, time.Minute)
	st.Interval = 100 * time.Millisecond
	st.BucketPeriod = 0
	g := newBreakerGroup([]gobreaker.Settings{st})
	cb := g.Get("/svc/X")

	for i := 0; i < 5; i++ {
		cb.Execute(func() (any, error) { return nil, errors.New("boom") })
	}

	// 固定窗口下 Interval 过了就清空
	time.Sleep(150 * time.Millisecond)
	cb.Execute(func() (any, error) { return nil, errors.New("boom") })
	if got := cb.Counts().Requests; got != 1 {
		t.Errorf("固定窗口应清空重算, got %+v", cb.Counts())
	}
}

// 没传 Settings 时用默认的滚动窗口。
func TestDefaultSettingsUseRollingWindow(t *testing.T) {
	g := newBreakerGroup(nil)
	cb := g.Get("/svc/X")

	for i := 0; i < 5; i++ {
		cb.Execute(func() (any, error) { return nil, errors.New("boom") })
	}
	if cb.Counts().Requests != 5 {
		t.Fatalf("应记了 5 次, got %+v", cb.Counts())
	}
	if cb.State() != gobreaker.StateClosed {
		t.Errorf("样本不够时不该熔断, state=%v", cb.State())
	}
}

// 中间件层面：连续 5xx 之后，被熔断的请求应直接被拒（不再打下游）。
func TestClientMiddlewareRejectsWhenOpen(t *testing.T) {
	mw := Client(testSettings(4, 0.5, time.Minute))

	var calls int64
	handler := func(ctx context.Context, req any) (any, error) {
		atomic.AddInt64(&calls, 1)
		return nil, kratoserrors.ServiceUnavailable("DOWNSTREAM", "下游挂了")
	}
	chain := mw(handler)

	ctx := transport.NewClientContext(context.Background(), &fakeTransport{operation: "/api.user.v1.User/GetUserByID"})

	for i := 0; i < 4; i++ {
		if _, err := chain(ctx, nil); err == nil {
			t.Fatal("下游返 5xx 时应返错")
		}
	}
	if got := atomic.LoadInt64(&calls); got != 4 {
		t.Fatalf("前 4 次应真的打到下游, calls=%d", got)
	}

	// 熔断打开后不该再打下游
	if _, err := chain(ctx, nil); err == nil {
		t.Fatal("熔断打开后应直接拒绝")
	}
	if got := atomic.LoadInt64(&calls); got != 4 {
		t.Fatalf("熔断打开后不应再打下游, calls=%d", got)
	}
}

// 熔断拒掉的请求要翻译成 503 —— 调用方看到的是「下游不可用」，
// 不是 gobreaker 的内部错误串。
func TestClientMiddlewareRejectionIs503(t *testing.T) {
	mw := Client(testSettings(2, 0.5, time.Minute))

	handler := func(ctx context.Context, req any) (any, error) {
		return nil, kratoserrors.ServiceUnavailable("DOWNSTREAM", "下游挂了")
	}
	chain := mw(handler)
	ctx := transport.NewClientContext(context.Background(), &fakeTransport{operation: "/svc/Reject"})

	for i := 0; i < 2; i++ {
		_, _ = chain(ctx, nil)
	}

	_, err := chain(ctx, nil)
	if err == nil {
		t.Fatal("熔断打开后应拒绝")
	}
	se := kratoserrors.FromError(err)
	if se.Code != 503 {
		t.Errorf("熔断拒绝应返 503, got %d (%v)", se.Code, err)
	}
	if se.Reason != "CIRCUITBREAKER" {
		t.Errorf("reason 应是 CIRCUITBREAKER, got %q", se.Reason)
	}
	if errors.Is(err, gobreaker.ErrOpenState) {
		t.Error("gobreaker 的内部错误不该透传给调用方")
	}
}

// 完整生命周期：熔断打开 → 拒绝 → 冷却期过 → 探针 → 恢复。
func TestBreakerOpenThenRecover(t *testing.T) {
	st := testSettings(3, 0.5, 100*time.Millisecond)
	st.Interval = time.Hour // 周期设长，避免清空干扰
	st.BucketPeriod = 0     // 固定窗口，避免滚动干扰
	mw := Client(st)

	var calls int
	downstreamUp := false
	handler := func(ctx context.Context, req any) (any, error) {
		calls++
		if !downstreamUp {
			return nil, kratoserrors.ServiceUnavailable("DOWNSTREAM", "下游挂了")
		}
		return "ok", nil
	}
	chain := mw(handler)
	ctx := transport.NewClientContext(context.Background(), &fakeTransport{operation: "/svc/Live"})

	// 连续失败 → 熔断
	for i := 0; i < 3; i++ {
		if _, err := chain(ctx, nil); err == nil {
			t.Fatal("应返错")
		}
	}
	if calls != 3 {
		t.Fatalf("应打了 3 次下游, calls=%d", calls)
	}

	// 熔断中 → 不打下游
	_, err := chain(ctx, nil)
	if err == nil {
		t.Fatal("应拒绝")
	}
	se := kratoserrors.FromError(err)
	if se.Code != 503 || se.Reason != "CIRCUITBREAKER" {
		t.Errorf("应返 503 CIRCUITBREAKER, got %d %s", se.Code, se.Reason)
	}
	if calls != 3 {
		t.Fatalf("熔断中不该打下游, calls=%d", calls)
	}

	// 下游恢复，但还在冷却期 → 仍拒绝
	downstreamUp = true
	if _, err := chain(ctx, nil); err == nil {
		t.Fatal("冷却期应仍拒绝")
	}
	if calls != 3 {
		t.Fatalf("冷却期不该打下游, calls=%d", calls)
	}

	// 冷却期过 → 半开 → 探针打下游 → 成功 → 关闭
	time.Sleep(150 * time.Millisecond)
	reply, err := chain(ctx, nil)
	if err != nil {
		t.Fatalf("冷却期后应放行探针, got %v", err)
	}
	if reply != "ok" {
		t.Errorf("探针应拿到下游结果, got %v", reply)
	}
	if calls != 4 {
		t.Fatalf("探针应打一次下游, calls=%d", calls)
	}

	// 关闭后正常放行
	if _, err := chain(ctx, nil); err != nil {
		t.Fatalf("关闭后应放行, got %v", err)
	}
	if calls != 5 {
		t.Fatalf("应再打一次下游, calls=%d", calls)
	}
}

// 业务错误（404）不是下游不健康，不该计入熔断统计。
func TestClientMiddlewareIgnoresBusinessErrors(t *testing.T) {
	mw := Client(testSettings(2, 0.5, time.Minute))

	var calls int64
	handler := func(ctx context.Context, req any) (any, error) {
		atomic.AddInt64(&calls, 1)
		return nil, kratoserrors.NotFound("NOT_FOUND", "用户不存在")
	}
	chain := mw(handler)
	ctx := transport.NewClientContext(context.Background(), &fakeTransport{operation: "/api.user.v1.User/GetUserByID"})

	for i := 0; i < 10; i++ {
		if _, err := chain(ctx, nil); err == nil {
			t.Fatal("应透传业务错误")
		}
	}
	// 404 不算失败 → 熔断器一直是闭合的 → 每次都真的打到了 handler
	if got := atomic.LoadInt64(&calls); got != 10 {
		t.Fatalf("404 不该触发熔断, calls=%d", got)
	}
}

// 调用方取消的请求不该触发熔断。
func TestClientMiddlewareIgnoresCanceledRequests(t *testing.T) {
	mw := Client(testSettings(2, 0.5, time.Minute))

	var calls int64
	handler := func(ctx context.Context, req any) (any, error) {
		atomic.AddInt64(&calls, 1)
		return nil, context.Canceled
	}
	chain := mw(handler)
	ctx := transport.NewClientContext(context.Background(), &fakeTransport{operation: "/svc/Cancel"})

	for i := 0; i < 10; i++ {
		_, _ = chain(ctx, nil)
	}
	if got := atomic.LoadInt64(&calls); got != 10 {
		t.Fatalf("取消的请求不该触发熔断, calls=%d", got)
	}
}

// 不同 operation 各自一个熔断器 —— 一个端点挂了不影响同一个下游的其它端点。
func TestClientMiddlewareIsolatesOperations(t *testing.T) {
	mw := Client(testSettings(2, 0.5, time.Minute))

	var mu sync.Mutex
	calls := map[string]int{}
	handler := func(ctx context.Context, req any) (any, error) {
		tr, _ := transport.FromClientContext(ctx)
		mu.Lock()
		calls[tr.Operation()]++
		mu.Unlock()
		return nil, kratoserrors.ServiceUnavailable("DOWNSTREAM", "下游挂了")
	}
	chain := mw(handler)

	badCtx := transport.NewClientContext(context.Background(), &fakeTransport{operation: "/svc/Bad"})
	otherCtx := transport.NewClientContext(context.Background(), &fakeTransport{operation: "/svc/Good"})

	for i := 0; i < 5; i++ {
		// 前两次真打下游，之后被熔断拒掉
		_, _ = chain(badCtx, nil)
	}

	mu.Lock()
	badCalls := calls["/svc/Bad"]
	mu.Unlock()
	if badCalls != 2 {
		t.Fatalf("Bad 应只真的打了 2 次下游（之后被熔断）, got %d", badCalls)
	}

	// 另一个 operation 不该被连坐 —— 它仍然会真的打到下游
	_, _ = chain(otherCtx, nil)
	mu.Lock()
	goodCalls := calls["/svc/Good"]
	mu.Unlock()
	if goodCalls != 1 {
		t.Fatalf("Good 应不受 Bad 熔断影响, calls=%d", goodCalls)
	}
}

// 并发下熔断器不能出乱子 —— 计数不能为负、状态不能跳变。
func TestClientMiddlewareConcurrent(t *testing.T) {
	mw := Client(testSettings(20, 0.5, 50*time.Millisecond))

	var calls int64
	handler := func(ctx context.Context, req any) (any, error) {
		atomic.AddInt64(&calls, 1)
		return nil, kratoserrors.ServiceUnavailable("DOWNSTREAM", "下游挂了")
	}
	chain := mw(handler)
	ctx := transport.NewClientContext(context.Background(), &fakeTransport{operation: "/svc/Concurrent"})

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_, _ = chain(ctx, nil)
			}
		}()
	}
	wg.Wait()

	// 并发跑了 1000 次，下游被打的次数不该超过 1000
	if got := atomic.LoadInt64(&calls); got > 1000 {
		t.Errorf("下游被打的次数超过调用次数, calls=%d", got)
	}
}
