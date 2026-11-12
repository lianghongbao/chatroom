package health_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"chatroom/pkg/health"
	"chatroom/pkg/middleware"

	"github.com/go-kratos/kratos/v3/middleware/selector"
	khttp "github.com/go-kratos/kratos/v3/transport/http"
)

// TestProbeEndpointsBypassAuthMiddleware 锁住一个关键假设：
//
// 探针端点**必须**用 srv.Handle 注册，不能用 srv.Route。
//
// srv.Route 是 proto 服务的注册方式 —— 生成的 handler 会调
// ctx.Middleware(...)，于是整条 Kratos 中间件链（含 JWT 鉴权）都会跑。
// 探针不带 token，走那条路会被 401 挡掉，K8s 就会认为 pod 挂了。
// srv.Handle 注册的是裸 http.Handler，完全绕过中间件链。
//
// 这个假设一旦被改坏（比如有人「顺手」把探针改成 Route），症状是
// 「pod 一直 NotReady」，很难第一时间联想到鉴权中间件。所以用测试钉死。
func TestProbeEndpointsBypassAuthMiddleware(t *testing.T) {
	const secret = "test-secret-at-least-32-bytes-long-value"

	// 生产同款鉴权：没有 Bearer token 一律 401。
	jwtMW := selector.Server(
		middleware.Server(secret, "chatroom", nil),
	).Match(func(_ context.Context, _ string) bool {
		return true
	}).Build()

	srv := khttp.NewServer(khttp.Middleware(jwtMW))

	// 对照组：Route 注册的端点（proto 服务的注册方式），会经过中间件链。
	// handler 里显式调 ctx.Middleware —— 生成的 *_http.pb.go 就是这么写的。
	r := srv.Route("/api")
	r.GET("/thing", func(ctx khttp.Context) error {
		h := ctx.Middleware(func(ctx context.Context, _ interface{}) (interface{}, error) {
			return map[string]string{"status": "ok"}, nil
		})
		out, err := h(ctx, nil)
		if err != nil {
			return err
		}
		return ctx.Result(http.StatusOK, out)
	})

	// 探针端点：srv.Handle 注册，绕过中间件链。
	srv.Handle("/healthz", health.Live())

	checker := health.NewChecker(time.Second)
	checker.Add("mysql", true, func(context.Context) error { return nil })
	srv.Handle("/readyz", checker.Ready())

	// 对照组先验：鉴权确实生效，否则下面的断言没有意义。
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/thing", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("Route 注册的端点应走鉴权中间件, got %d", rec.Code)
	}

	for _, path := range []string{"/healthz", "/readyz"} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s 不该要求 token, got %d", path, rec.Code)
		}
	}
}
