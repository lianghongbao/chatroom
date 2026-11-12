package server

import (
	convv1 "chatroom/conversation-service/api/conversation/v1"
	"chatroom/conversation-service/internal/conf"
	"chatroom/conversation-service/internal/middleware"
	"chatroom/conversation-service/internal/observability"
	"chatroom/conversation-service/internal/service"

	kmiddleware "github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/middleware/recovery"
	"github.com/go-kratos/kratos/v3/transport/grpc"
)

// NewGRPCServer 构造 gRPC server。
func NewGRPCServer(c *conf.Server, ac *conf.Auth, obs *observability.Provider, store middleware.TokenChecker, svc *service.ConversationService) *grpc.Server {
	issuer := ac.GetTokenIssuer()
	skip := append([]string(nil), ac.GetSkipOperations()...)
	skip = append(skip, InternalOps()...)

	mws := []kmiddleware.Middleware{
		recovery.Recovery(),
		middleware.Server(ac.GetJwtSecret(), issuer, store, skip),
		// HTTP transport 的 Operation 是 URL 路径模板，不是 gRPC 的 operation 名，
		// 所以内部凭证校验的 op 列表里两份都要有。
		middleware.InternalServer(ac.GetInternalSecret(), InternalOps()...),
	}
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
