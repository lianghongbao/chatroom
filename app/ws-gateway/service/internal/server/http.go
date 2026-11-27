package server

import (
	"chatroom/app/ws-gateway/service/internal/conf"
	"chatroom/app/ws-gateway/service/internal/gateway"
	"chatroom/pkg/health"
	"chatroom/pkg/observability"

	kmiddleware "github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/middleware/recovery"
	khttp "github.com/go-kratos/kratos/v3/transport/http"
)

// NewHTTPServer 构造 HTTP server。
// c 服务配置
// obs 观测能力
// gw 网关
// checker 健康检查器
func NewHTTPServer(c *conf.Server, obs *observability.Provider, gw *gateway.Server, checker *health.Checker) *khttp.Server {
	mws := []kmiddleware.Middleware{
		recovery.Recovery(),
	}
	mws = append(mws, obs.Middlewares()...)

	var opts = []khttp.ServerOption{khttp.Middleware(mws...)}
	if c.GetHttp().GetNetwork() != "" {
		opts = append(opts, khttp.Network(c.GetHttp().GetNetwork()))
	}
	if c.GetHttp().GetAddr() != "" {
		opts = append(opts, khttp.Address(c.GetHttp().GetAddr()))
	}
	if c.GetHttp().GetTimeout() != nil {
		opts = append(opts, khttp.Timeout(c.GetHttp().GetTimeout().AsDuration()))
	}
	srv := khttp.NewServer(opts...)

	// WS 端点。用 Handle 而不是 Route —— Route 是给 proto 服务注册用的
	srv.Handle(gw.Path(), gw.Handler())

	// 健康检查：K8s / docker healthcheck 要有端点可探，且必须在 JWT 中间件之前生效（探针不带 token）
	// /healthz 是 liveness —— 只报进程活着，不碰任何依赖
	// /readyz 是 readiness —— 探依赖，硬依赖挂了返 503，pod 离开 endpoints
	srv.Handle("/healthz", health.Live())
	srv.Handle("/readyz", checker.Ready())

	return srv
}
