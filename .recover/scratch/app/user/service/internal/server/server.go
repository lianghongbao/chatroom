// Package server 是 user 域的 server 装配层：构造 HTTP/gRPC server 并注册本域路由
package server

import (
	"context"

	"chatroom/user-service/internal/conf"
	"chatroom/user-service/internal/middleware"

	kmiddleware "github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/middleware/selector"
	"github.com/google/wire"
)

// InternalOps 是 user 域「仅供服务间调用」的端点，列的是 proto operation 名。
//
// HTTP transport 报的 operation 也是 proto 名 —— 生成的 handler 会调
// http.SetOperation 把它覆盖回去，所以不用另外列 HTTP 路径模板。
//
// 这些端点走内部凭证（X-Internal-Token），不走用户 JWT
const (
	InternalOpGetFriendProfiles             = "/api.user.v1.User/GetFriendProfiles"
	InternalOpGetConversationMemberProfiles = "/api.user.v1.User/GetConversationMemberProfiles"
)

// InternalOps 该域全部内部端点
func InternalOps() []string {
	return []string{
		InternalOpGetFriendProfiles,
		InternalOpGetConversationMemberProfiles,
	}
}

// authMiddlewares 返回鉴权相关的两个中间件：用户 JWT 与内部凭证。
//
// 白名单（免 JWT）由官方 selector 的 Match 圈定 —— selector 的语义是
// 「匹配上才执行中间件」，所以这里取反：不在白名单里的 operation 走 JWT。
//
// 白名单三部分：配置里的 auth.skip_operations、RefreshToken（带的是 refresh
// token，不是 access token）、本域内部端点（走内部凭证，不走用户 JWT）。
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
