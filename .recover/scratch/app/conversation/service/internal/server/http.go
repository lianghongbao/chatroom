package server

import (
	"net/http"

	convv1 "chatroom/conversation-service/api/conversation/v1"
	"chatroom/conversation-service/internal/conf"
	"chatroom/conversation-service/internal/middleware"
	"chatroom/conversation-service/internal/observability"
	"chatroom/conversation-service/internal/service"

	kmiddleware "github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/middleware/recovery"
	"github.com/go-kratos/kratos/v3/middleware/validate"
	khttp "github.com/go-kratos/kratos/v3/transport/http"

	"go.einride.tech/aip/fieldbehavior"
	"google.golang.org/protobuf/proto"
)

// NewHTTPServer 构造 HTTP server。
func NewHTTPServer(c *conf.Server, ac *conf.Auth, obs *observability.Provider, store middleware.TokenChecker, svc *service.ConversationService) *khttp.Server {
	issuer := ac.GetTokenIssuer()
	skip := append([]string(nil), ac.GetSkipOperations()...)
	skip = append(skip, InternalOps()...)

	mws := []kmiddleware.Middleware{
		recovery.Recovery(),
		middleware.Server(ac.GetJwtSecret(), issuer, store, skip),
		// HTTP transport 的 Operation 是 URL 路径模板，所以 op 列表里 gRPC
		// operation 名和 HTTP 路径模板两份都要有。
		middleware.InternalServer(ac.GetInternalSecret(), InternalOps()...),
		validate.Validator(func(req any) error {
			if msg, ok := req.(proto.Message); ok {
				if err := fieldbehavior.ValidateRequiredFields(msg); err != nil {
					return err
				}
			}
			return nil
		}),
	}
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
	convv1.RegisterConversationHTTPServer(srv, svc)
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
