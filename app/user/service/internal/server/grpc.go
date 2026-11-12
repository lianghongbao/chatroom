package server

import (
	v1 "chatroom/api/user/service/v1"
	"chatroom/app/user/service/internal/conf"
	"chatroom/app/user/service/internal/service"
	"chatroom/pkg/middleware"
	"chatroom/pkg/observability"

	kmiddleware "github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/middleware/recovery"
	"github.com/go-kratos/kratos/v3/transport/grpc"
)

// NewGRPCServer 构造 gRPC server。
func NewGRPCServer(c *conf.Server, ac *conf.Auth, obs *observability.Provider, store middleware.TokenChecker, svc *service.UserService) *grpc.Server {
	// tracing 必须在最外层：它包住后面的鉴权中间件，401 才会进 trace。
	// 未鉴权的 span 由 observability 的过滤层按采样率丢弃。
	mws := []kmiddleware.Middleware{recovery.Recovery()}
	mws = append(mws, obs.Middlewares()...)
	mws = append(mws, authMiddlewares(ac, store)...)

	var opts = []grpc.ServerOption{
		grpc.Middleware(mws...),
	}
	if c.GetGrpc().GetNetwork() != "" {
		opts = append(opts, grpc.Network(c.GetGrpc().GetNetwork()))
	}
	if c.GetGrpc().GetAddr() != "" {
		opts = append(opts, grpc.Address(c.GetGrpc().GetAddr()))
	}
	if c.GetGrpc().GetTimeout() != nil {
		opts = append(opts, grpc.Timeout(c.GetGrpc().GetTimeout().AsDuration()))
	}
	srv := grpc.NewServer(opts...)
	v1.RegisterUserServer(srv, svc)
	return srv
}
