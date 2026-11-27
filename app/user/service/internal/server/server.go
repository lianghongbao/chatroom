// Package server 是 user 域的 server 装配层：构造 HTTP/gRPC server 并注册本域路由。
package server

import (
	"context"

	"chatroom/app/user/service/internal/conf"
	"chatroom/pkg/middleware"

	kmiddleware "github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/middleware/selector"
	"github.com/google/wire"
)

// InternalOps 是 user 域「仅供服务间调用」的端点，列的是 proto operation 名
// （HTTP transport 报的 operation 也是 proto 名）。这些端点走内部凭证，不走用户 JWT。
const (
	InternalOpGetFriendProfiles             = "/api.user.v1.User/GetFriendProfiles"
	InternalOpGetConversationMemberProfiles = "/api.user.v1.User/GetConversationMemberProfiles"
)

// InternalOps 返回该域全部内部端点的 operation 名。
func InternalOps() []string {
	return []string{
		InternalOpGetFriendProfiles,
		InternalOpGetConversationMemberProfiles,
	}
}

// authMiddlewares 返回鉴权相关的两个中间件：用户 JWT 与内部凭证。
// ac 鉴权配置段
// store token 校验器
// 白名单（免 JWT）三部分：auth.skip_operations、RefreshToken、本域内部端点。
func authMiddlewares(ac *conf.Auth, store middleware.TokenChecker) []kmiddleware.Middleware {
	issuer := ac.GetTokenIssuer()
	whitelist := append([]string(nil), ac.GetSkipOperations()...)
	whitelist = append(whitelist, "/api.user.v1.User/RefreshToken")
	whitelist = append(whitelist, InternalOps()...)

	jwtMW := selector.Server(
		middleware.Server(ac.GetJwtSecret(), issuer, store),
	).Match(func(_ context.Context, operation string) bool {
		return !middleware.OpInList(operation, whitelist)
	}).Build()

	// 内部凭证只管辖本域内部端点 —— 语义与 selector 一致：匹配上才校验。
	internalMW := selector.Server(
		middleware.InternalServer(ac.GetInternalSecret()),
	).Match(func(_ context.Context, operation string) bool {
		return middleware.OpInList(operation, InternalOps())
	}).Build()

	return []kmiddleware.Middleware{jwtMW, internalMW}
}

// ProviderSet 是 server 层的 providers。
var ProviderSet = wire.NewSet(NewGRPCServer, NewHTTPServer)
