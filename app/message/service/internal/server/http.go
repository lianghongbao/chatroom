package server

import (
	"net/http"

	msgv1 "chatroom/api/message/service/v1"
	"chatroom/app/message/service/internal/conf"
	"chatroom/app/message/service/internal/service"
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
// ac 鉴权配置
// obs 观测能力
// store access token 黑名单查询
// svc 本域的 HTTP 服务实现
func NewHTTPServer(c *conf.Server, ac *conf.Auth, obs *observability.Provider, store middleware.TokenChecker, svc *service.MessageService) *khttp.Server {
	mws := []kmiddleware.Middleware{
		recovery.Recovery(),
	}
	mws = append(mws, authMiddlewares(ac, store)...)
	mws = append(mws, []kmiddleware.Middleware{
		validate.Validator(func(req any) error {
			if msg, ok := req.(proto.Message); ok {
				if err := fieldbehavior.ValidateRequiredFields(msg); err != nil {
					return err
				}
			}
			return nil
		}),
	}...)
	mws = append(mws, obs.Middlewares()...)

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
	// 健康检查。必须在 JWT 中间件之前生效（探针不带 token）。
	srv.Handle("/healthz", httpHealthHandler())
	msgv1.RegisterMessageHTTPServer(srv, svc)
	return srv
}

// httpHealthHandler 健康检查 handler。只报进程存活，不探测任何依赖。
func httpHealthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}
}
