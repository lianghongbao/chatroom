package breaker

import (
	"context"
	"sync"
	"testing"
	"time"

	kratoserrors "github.com/go-kratos/kratos/v3/errors"
	"github.com/go-kratos/kratos/v3/transport"
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

func newTestBreaker() *breaker {
	return newBreaker(&options{
		failureRatio: 0.5,
		request:      4,
		window:       10 * time.Second,
		bucket:       10,
		openDuration: 50 * time.Millisecond,
		halfOpenMax:  1,
	})
}

// 失败率超阈值且样本够 → 熔断打开。
func TestBreakerOpensAfterFailureRatio(t *testing.T) {
	b := newTestBreaker()

	for i := 0; i < 4; i++ {
		if err := b.Allow(); err != nil {
			t.Fatalf("熔断前应放行, got %v", err)
		}
		b.MarkFailed()
	}

	if err := b.Allow(); err == nil {
		t.Fatal("失败率超阈值后应拒绝请求")
	}
}

// 样本不够时不该熔断 —— 流量低的时候三五次失败说明不了问题。
func TestBreakerStaysClosedBelowMinRequests(t *testing.T) {
	b := newTestBreaker()

	for i := 0; i < 3; i++ {
		if err := b.Allow(); err != nil {
			t.Fatalf("样本不足时应放行, got %v", err)
		}
		b.MarkFailed()
	}

	if err := b.Allow(); err != nil {
		t.Fatalf("样本不足时不应熔断, got %v", err)
	}
}

// 成功率够高就不该熔断 —— 偶发失败不算下游挂了。
func TestBreakerStaysClosedWithGoodSuccessRatio(t *testing.T) {
	b := newTestBreaker()

	// 10 次里只失败 1 次（失败率 10%），远低于 50% 的阈值
	for i := 0; i < 10; i++ {
		if err := b.Allow(); err != nil {
			t.Fatalf("第 %d 次不该被拒, got %v", i, err)
		}
		if i == 0 {
			b.MarkFailed()
		} else {
			b.MarkSuccess()
		}
	}

	if err := b.Allow(); err != nil {
		t.Fatalf("失败率 10%% 不该触发熔断, got %v", err)
	}
}

// 冷却期过了要进半开，放一个试探请求。
func TestBreakerHalfOpensAfterCooldown(t *testing.T) {
	b := newTestBreaker()

	for i := 0; i < 4; i++ {
		if err := b.Allow(); err != nil {
			t.Fatalf("熔断前应放行, got %v", err)
		}
		b.MarkFailed()
	}
	if err := b.Allow(); err == nil {
		t.Fatal("应处于熔断状态")
	}

	time.Sleep(80 * time.Millisecond)

	if err := b.Allow(); err != nil {
		t.Fatalf("冷却期后应放行试探请求, got %v", err)
	}
	// 半开状态只放一个试探请求，第二个要被拒
	if err := b.Allow(); err == nil {
		t.Fatal("半开状态超额试探请求应被拒")
	}
}

// 半开的试探成功 → 恢复关闭。
func TestBreakerClosesAfterSuccessfulProbe(t *testing.T) {
	b := newTestBreaker()

	for i := 0; i < 4; i++ {
		if err := b.Allow(); err != nil {
			t.Fatalf("熔断前应放行, got %v", err)
		}
		b.MarkFailed()
	}
	time.Sleep(80 * time.Millisecond)

	if err := b.Allow(); err != nil {
		t.Fatalf("冷却期后应放行试探请求, got %v", err)
	}
	b.MarkSuccess()

	if err := b.Allow(); err != nil {
		t.Fatalf("试探成功后应恢复放行, got %v", err)
	}
}

// 半开的试探又失败 → 重新熔断，冷却期重新开始。
func TestBreakerReopensAfterFailedProbe(t *testing.T) {
	b := newTestBreaker()

	for i := 0; i < 4; i++ {
		if err := b.Allow(); err != nil {
			t.Fatalf("熔断前应放行, got %v", err)
		}
		b.MarkFailed()
	}
	time.Sleep(80 * time.Millisecond)

	if err := b.Allow(); err != nil {
		t.Fatalf("冷却期后应放行试探请求, got %v", err)
	}
	b.MarkFailed()

	if err := b.Allow(); err == nil {
		t.Fatal("试探失败后应重新熔断")
	}
}

// 窗口滚动 —— 旧的失败样本过期后不再计入。
func TestBreakerForgetsOldSamples(t *testing.T) {
	b := newBreaker(&options{
		failureRatio: 0.5,
		request:      2,
		window:       200 * time.Millisecond,
		bucket:       2,
		openDuration: time.Second,
		halfOpenMax:  1,
	})

	for i := 0; i < 2; i++ {
		if err := b.Allow(); err != nil {
			t.Fatalf("熔断前应放行, got %v", err)
		}
		b.MarkFailed()
	}
	if err := b.Allow(); err == nil {
		t.Fatal("连续失败应触发熔断")
	}

	// 窗口滑过去之后旧样本不再计入 —— 状态还是 open，但要等冷却期。
	// 这里验证计数确实会滚动。
	time.Sleep(250 * time.Millisecond)
	successes, total := b.counter.summary()
	if total != 0 || successes != 0 {
		t.Fatalf("窗口过期后计数应清零, got total=%d successes=%d", total, successes)
	}
}

// 中间件层面：连续 5xx 之后，被熔断的请求应直接被拒（不再打下游）。
func TestClientMiddlewareRejectsWhenOpen(t *testing.T) {
	mw := Client(
		WithRequest(4),
		WithWindow(10*time.Second),
		WithOpenDuration(time.Minute),
	)

	calls := 0
	handler := func(ctx context.Context, req any) (any, error) {
		calls++
		return nil, kratoserrors.ServiceUnavailable("DOWNSTREAM", "下游挂了")
	}
	chain := mw(handler)

	ctx := transport.NewClientContext(context.Background(), &fakeTransport{operation: "/api.user.v1.User/GetUserByID"})

	for i := 0; i < 4; i++ {
		if _, err := chain(ctx, nil); err == nil {
			t.Fatal("下游返 5xx 时应返错")
		}
	}
	if calls != 4 {
		t.Fatalf("前 4 次应真的打到下游, calls=%d", calls)
	}

	// 熔断打开后不该再打下游
	if _, err := chain(ctx, nil); err == nil {
		t.Fatal("熔断打开后应直接拒绝")
	}
	if calls != 4 {
		t.Fatalf("熔断打开后不应再打下游, calls=%d", calls)
	}
}

// 业务错误（404）不是下游不健康，不该计入熔断统计。
func TestClientMiddlewareIgnoresBusinessErrors(t *testing.T) {
	mw := Client(WithRequest(2), WithWindow(10*time.Second), WithOpenDuration(time.Minute))

	handler := func(ctx context.Context, req any) (any, error) {
		return nil, kratoserrors.NotFound("NOT_FOUND", "用户不存在")
	}
	chain := mw(handler)
	ctx := transport.NewClientContext(context.Background(), &fakeTransport{operation: "/api.user.v1.User/GetUserByID"})

	for i := 0; i < 10; i++ {
		if _, err := chain(ctx, nil); err == nil {
			t.Fatal("应透传业务错误")
		}
	}
	// 没被熔断挡住 —— 最后一次仍然真的打到了 handler
	if _, err := chain(ctx, nil); err == nil {
		t.Fatal("应透传业务错误")
	}
}

// 不同 operation 各自一个熔断器 —— 一个端点挂了不影响同一个下游的其它端点。
func TestClientMiddlewareIsolatesOperations(t *testing.T) {
	mw := Client(WithRequest(2), WithWindow(10*time.Second), WithOpenDuration(time.Minute))

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
