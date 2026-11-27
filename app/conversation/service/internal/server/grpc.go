package server

import (
	convv1 "chatroom/api/conversation/service/v1"
	"chatroom/app/conversation/service/internal/conf"
	"chatroom/app/conversation/service/internal/service"
	"chatroom/pkg/middleware"
	"chatroom/pkg/observability"

	kmiddleware "github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/middleware/recovery"
	"github.com/go-kratos/kratos/v3/transport/grpc"
)

// NewGRPCServer 构造 gRPC server。
// c 服务配置
// ac 鉴权配置
// obs 观测能力
// store token 黑名单查询器
// svc 会话服务
func NewGRPCServer(c *conf.Server, ac *conf.Auth, obs *observability.Provider, store middleware.TokenChecker, svc *service.ConversationService) *grpc.Server {
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
	convv1.RegisterConversationServer(srv, svc)
	return srv
}
