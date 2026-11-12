package server

import (
	friendv1 "chatroom/api/friend/service/v1"
	"chatroom/app/friend/service/internal/conf"
	"chatroom/app/friend/service/internal/service"
	"chatroom/pkg/middleware"
	"chatroom/pkg/observability"

	kmiddleware "github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/middleware/recovery"
	"github.com/go-kratos/kratos/v3/transport/grpc"
)

// NewGRPCServer 构造 gRPC server。
func NewGRPCServer(c *conf.Server, ac *conf.Auth, obs *observability.Provider, store middleware.TokenChecker, svc *service.FriendService) *grpc.Server {
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
	friendv1.RegisterFriendServer(srv, svc)
	return srv
}
