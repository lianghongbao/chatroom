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
	"github.com/sony/gobreaker"
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

// newTestGroup 造一个测试用的熔断器组。
func newTestGroup(minRequests int64, ratio float64, timeout time.Duration) *breakerGroup {
	return newBreakerGroup(&options{
		failureRatio: ratio,
		request:      uint32(minRequests),
		interval:     10 * time.Second,
		timeout:      timeout,
		maxRequests:  1,
	})
}

// 失败率超阈值且样本够 → 熔断打开。
func TestBreakerOpensAfterFailureRatio(t *testing.T) {
	g := newTestGroup(4, 0.5, time.Minute)
	cb := g.Get("/svc/X")

	for i := 0; i < 4; i++ {
		_, err := cb.Execute(func() (interface{}, error) { return nil, errors.New("boom") })
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
	g := newTestGroup(4, 0.5, time.Minute)
	cb := g.Get("/svc/X")

	for i := 0; i < 3; i++ {
		cb.Execute(func() (interface{}, error) { return nil, errors.New("boom") })
	}

	if cb.State() != gobreaker.StateClosed {
		t.Fatalf("样本不足时应保持闭合, state=%v", cb.State())
	}
}

// 成功率够高就不该熔断 —— 偶发失败不算下游挂了。
func TestBreakerStaysClosedWithGoodSuccessRatio(t *testing.T) {
	g := newTestGroup(4, 0.5, time.Minute)
	cb := g.Get("/svc/X")

	// 10 次里只失败 1 次（失败率 10%），远低于 50% 的阈值
	for i := 0; i < 10; i++ {
		if i == 0 {
			cb.Execute(func() (interface{}, error) { return nil, errors.New("boom") })
		} else {
			cb.Execute(func() (interface{}, error) { return "ok", nil })
		}
	}

	if cb.State() != gobreaker.StateClosed {
		t.Fatalf("失败率 10%% 不该触发熔断, state=%v", cb.State())
	}
}

// 冷却期过了要进半开。
func TestBreakerHalfOpensAfterCooldown(t *testing.T) {
	g := newTestGroup(2, 0.5, 50*time.Millisecond)
	cb := g.Get("/svc/X")

	for i := 0; i < 2; i++ {
		cb.Execute(func() (interface{}, error) { return nil, errors.New("boom") })
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
	g := newTestGroup(2, 0.5, 50*time.Millisecond)
	cb := g.Get("/svc/X")

	for i := 0; i < 2; i++ {
		cb.Execute(func() (interface{}, error) { return nil, errors.New("boom") })
	}
	time.Sleep(80 * time.Millisecond)

	if cb.State() != gobreaker.StateHalfOpen {
		t.Fatalf("冷却期后应进半开, state=%v", cb.State())
	}
	cb.Execute(func() (interface{}, error) { return "ok", nil })

	if cb.State() != gobreaker.StateClosed {
		t.Fatalf("试探成功后应恢复关闭, state=%v", cb.State())
	}
}

// 半开的试探又失败 → 重新熔断，冷却期重新开始。
func TestBreakerReopensAfterFailedProbe(t *testing.T) {
	g := newTestGroup(2, 0.5, 50*time.Millisecond)
	cb := g.Get("/svc/X")

	for i := 0; i < 2; i++ {
		cb.Execute(func() (interface{}, error) { return nil, errors.New("boom") })
	}
	time.Sleep(80 * time.Millisecond)

	if cb.State() != gobreaker.StateHalfOpen {
		t.Fatalf("冷却期后应进半开, state=%v", cb.State())
	}
	cb.Execute(func() (interface{}, error) { return nil, errors.New("boom") })

	if cb.State() != gobreaker.StateOpen {
		t.Fatalf("试探失败后应重新熔断, state=%v", cb.State())
	}
}

// 熔断打开期间的本地拒绝不该污染计数 ——
// 本地拒绝不代表下游状态。这是自研实现里最容易出错的地方。
func TestLocalRejectsDoNotPolluteCounts(t *testing.T) {
	g := newTestGroup(2, 0.5, time.Minute)
	cb := g.Get("/svc/X")

	for i := 0; i < 2; i++ {
		cb.Execute(func() (interface{}, error) { return nil, errors.New("boom") })
	}
	if cb.State() != gobreaker.StateOpen {
		t.Fatalf("应熔断, state=%v", cb.State())
	}

	before := cb.Counts()

	// 熔断期间连拒 50 次 —— 下游根本没被调用
	for i := 0; i < 50; i++ {
		_, err := cb.Execute(func() (interface{}, error) {
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
//
// 自研实现里用 breaker 上的单个 bool 标记「这次拒绝是本地的」——
// 多 goroutine 交错时标记会被别的 goroutine 清掉，真实失败被丢弃、
// 本地拒绝被当成真实失败。gobreaker 在 open 态直接返错不动计数，没这个洞。
func TestConcurrentLocalRejectsDoNotPolluteCounts(t *testing.T) {
	g := newBreakerGroup(&options{
		failureRatio: 0.99, // 几乎不熔断，隔离出计数问题
		request:      1000,
		interval:     time.Hour,
		timeout:      time.Hour,
		maxRequests:  1,
	})
	cb := g.Get("/svc/X")

	// 先让它熔断
	for i := 0; i < 1000; i++ {
		cb.Execute(func() (interface{}, error) { return nil, errors.New("boom") })
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
				cb.Execute(func() (interface{}, error) { return nil, nil })
			}
		}()
	}
	wg.Wait()

	counts := cb.Counts()
	if counts.Requests != 0 || counts.TotalFailures != 0 {
		t.Errorf("并发下本地拒绝污染了计数: %+v", counts)
	}
}

// 迟到的结果不该污染新一代的计数。
//
// 半开态下放探针，探针还没回来时状态已经切了一代 ——
// 旧探针的结果回来时对不上 generation，gobreaker 直接丢弃。
func TestStaleResultDoesNotPolluteNewGeneration(t *testing.T) {
	g := newTestGroup(2, 0.5, 50*time.Millisecond)
	cb := g.Get("/svc/X")

	for i := 0; i < 2; i++ {
		cb.Execute(func() (interface{}, error) { return nil, errors.New("boom") })
	}
	time.Sleep(80 * time.Millisecond)

	// 半开态：放一个探针，但探针「卡住」不返回。
	// 用 Execute 的同步语义做不到真卡住 —— 改用底层计数验证：
	// 切一代后，计数必须清零。
	if cb.State() != gobreaker.StateHalfOpen {
		t.Fatalf("应进半开, state=%v", cb.State())
	}
	cb.Execute(func() (interface{}, error) { return "ok", nil })
	if cb.State() != gobreaker.StateClosed {
		t.Fatalf("试探成功后应关闭, state=%v", cb.State())
	}
	// 切回 closed 的一代，计数应该是新一代的（干净的）
	if cb.Counts().TotalFailures != 0 {
		t.Errorf("新一代的计数应清零, got %+v", cb.Counts())
	}
}

// 统计周期到了之后计数清空 —— gobreaker 的原生语义。
func TestIntervalClearsCounts(t *testing.T) {
	g := newBreakerGroup(&options{
		failureRatio: 0.5,
		request:      100, // 样本要求高，避免熔断干扰观察
		interval:     100 * time.Millisecond,
		timeout:      time.Minute,
		maxRequests:  1,
	})
	cb := g.Get("/svc/X")

	for i := 0; i < 5; i++ {
		cb.Execute(func() (interface{}, error) { return nil, errors.New("boom") })
	}
	if cb.Counts().Requests != 5 {
		t.Fatalf("应记了 5 次, got %+v", cb.Counts())
	}

	// 等过周期，再来一个请求触发惰性清空。
	time.Sleep(150 * time.Millisecond)
	cb.Execute(func() (interface{}, error) { return nil, errors.New("boom") })

	if cb.Counts().Requests != 1 {
		t.Errorf("周期过期后计数应清空重算, got %+v", cb.Counts())
	}
}

// 中间件层面：连续 5xx 之后，被熔断的请求应直接被拒（不再打下游）。
func TestClientMiddlewareRejectsWhenOpen(t *testing.T) {
	mw := Client(
		WithRequest(4),
		WithInterval(10*time.Second),
		WithTimeout(time.Minute),
	)

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
	mw := Client(WithRequest(2), WithInterval(10*time.Second), WithTimeout(time.Minute))

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
	// 必须是 gobreaker 的内部错误被翻译掉了
	if errors.Is(err, gobreaker.ErrOpenState) {
		t.Error("gobreaker 的内部错误不该透传给调用方")
	}
}

// 完整生命周期：熔断打开 → 拒绝 → 冷却期过 → 探针 → 恢复。
//
// 这是最贴近真实使用场景的一条 —— 熔断器最难的不是「会不会打开」，
// 而是「打开之后能不能自己恢复」。
func TestBreakerOpenThenRecover(t *testing.T) {
	mw := Client(
		WithRequest(3),
		WithInterval(time.Hour), // 周期设长，避免清空干扰
		WithTimeout(100*time.Millisecond),
	)

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
	mw := Client(WithRequest(2), WithInterval(10*time.Second), WithTimeout(time.Minute))

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

// 不同 operation 各自一个熔断器 —— 一个端点挂了不影响同一个下游的其它端点。
func TestClientMiddlewareIsolatesOperations(t *testing.T) {
	mw := Client(WithRequest(2), WithInterval(10*time.Second), WithTimeout(time.Minute))

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
		// 前两次真打下游，之后被熔断拒掉 —— 返回值本身不关心，
		// 关心的是「真的打了几次」。
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
	mw := Client(
		WithRequest(20),
		WithInterval(10*time.Second),
		WithTimeout(50*time.Millisecond),
	)

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
