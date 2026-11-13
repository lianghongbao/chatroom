package server

import (
	"net/http"

	v1 "chatroom/api/user/service/v1"
	"chatroom/app/user/service/internal/conf"
	"chatroom/app/user/service/internal/service"
	"chatroom/pkg/middleware"
	"chatroom/pkg/observability"

	kmiddleware "github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/middleware/recovery"
	"github.com/go-kratos/kratos/v3/middleware/validate"
	khttp "github.com/go-kratos/kratos/v3/transport/http"

	"go.einride.tech/aip/fieldbehavior"
	"google.golang.org/protobuf/proto"
)

// NewHTTPServer 构造 HTTP server。
// c server 配置段
// ac 鉴权配置段
// obs 观测能力
// store token 校验器
// svc 本域的业务服务。
func NewHTTPServer(c *conf.Server, ac *conf.Auth, obs *observability.Provider, store middleware.TokenChecker, svc *service.UserService) *khttp.Server {
	// tracing 必须在最外层：它包住后面的鉴权中间件，401 才会进 trace。
	// 未鉴权的 span 由 observability 的过滤层按采样率丢弃。
	mws := []kmiddleware.Middleware{recovery.Recovery()}
	mws = append(mws, obs.Middlewares()...)
	mws = append(mws, authMiddlewares(ac, store)...)
	mws = append(mws, kmiddleware.Middleware(validate.Validator(func(req any) error {
		if msg, ok := req.(proto.Message); ok {
			if err := fieldbehavior.ValidateRequiredFields(msg); err != nil {
				return err
			}
		}
		return nil
	})))

	var opts = []khttp.ServerOption{
		khttp.Middleware(mws...),
	}
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
	// 健康检查：K8s / docker healthcheck 要有端点可探，且必须在 JWT 中间件之前生效（探针不带 token）
	srv.Handle("/healthz", httpHealthHandler())
	v1.RegisterUserHTTPServer(srv, svc)
	return srv
}

// httpHealthHandler 健康检查 handler，固定返 200 与 {"status":"ok"}。
func httpHealthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}
}
