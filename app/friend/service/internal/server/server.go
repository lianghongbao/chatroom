// Package server 是 friend 域的 server 装配层：构造 HTTP/gRPC server 并注册路由。
package server

import (
	"context"

	userv1 "chatroom/api/user/service/v1"
	"chatroom/app/friend/service/internal/conf"
	"chatroom/pkg/middleware"

	kmiddleware "github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/middleware/selector"
	"github.com/google/wire"
)

// InternalOps 是 friend 域「仅供服务间调用」的端点，列的是 proto operation 名。
//
// 走内部凭证（X-Internal-Token），不走用户 JWT。
func InternalOps() []string {
	return []string{}
}

// authMiddlewares 返回鉴权相关的两个中间件：用户 JWT 与内部凭证。
//
// 白名单（免 JWT）由官方 selector 的 Match 圈定 —— selector 的语义是
// 「匹配上才执行中间件」，所以这里取反：不在白名单里的 operation 走 JWT。
func authMiddlewares(ac *conf.Auth, store middleware.TokenChecker) []kmiddleware.Middleware {
	issuer := ac.GetTokenIssuer()
	whitelist := append([]string(nil), ac.GetSkipOperations()...)
	whitelist = append(whitelist, InternalOps()...)

	jwtMW := selector.Server(
		middleware.Server(ac.GetJwtSecret(), issuer, store,
			// 拉黑是 user 域的动作（user 服务写入黑名单），但每个服务都要查 ——
			// 原因字符串属于 user 域的契约，由调用方传进来。
			middleware.WithBlacklistedReason(userv1.ErrorReason_USER_TOKEN_BLACKLISTED.String())),
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
