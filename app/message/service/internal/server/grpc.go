package server

import (
	msgv1 "chatroom/api/message/service/v1"
	"chatroom/app/message/service/internal/conf"
	"chatroom/app/message/service/internal/service"
	"chatroom/pkg/middleware"
	"chatroom/pkg/observability"

	kmiddleware "github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/middleware/recovery"
	"github.com/go-kratos/kratos/v3/transport/grpc"
)

// NewGRPCServer 构造 gRPC server。
// c server 配置段
// ac 鉴权配置
// obs 观测能力
// store 查 access token 黑名单
// svc 本域的 gRPC 服务实现
func NewGRPCServer(c *conf.Server, ac *conf.Auth, obs *observability.Provider, store middleware.TokenChecker, svc *service.MessageService) *grpc.Server {
	mws := []kmiddleware.Middleware{
		recovery.Recovery(),
	}
	mws = append(mws, authMiddlewares(ac, store)...)
	mws = append(mws, obs.Middlewares()...)

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
	msgv1.RegisterMessageServer(srv, svc)
	return srv
}
