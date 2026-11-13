package server

import (
	"net/http"

	"chatroom/app/ws-gateway/service/internal/conf"
	"chatroom/app/ws-gateway/service/internal/gateway"
	"chatroom/pkg/observability"

	kmiddleware "github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/middleware/recovery"
	khttp "github.com/go-kratos/kratos/v3/transport/http"
)

// NewHTTPServer 构造 HTTP server。
// c 服务配置
// obs 观测能力
// gw 网关
// handler WS 升级 handler
func NewHTTPServer(c *conf.Server, obs *observability.Provider, gw *gateway.Server) *khttp.Server {
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

	// 健康检查。网关不注册 proto 服务，得自己加一个
	srv.Handle("/healthz", httpHealthHandler())

	return srv
}

// httpHealthHandler 健康检查 handler。
func httpHealthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}
}
