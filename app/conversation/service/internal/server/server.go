// Package server 是 conversation 域的 server 装配层：构造 HTTP/gRPC server 并注册路由。
package server

import (
	"context"

	userv1 "chatroom/api/user/service/v1"
	"chatroom/app/conversation/service/internal/conf"
	"chatroom/pkg/middleware"

	kmiddleware "github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/middleware/selector"
	"github.com/google/wire"
)

// conversation 域「仅供服务间调用」的端点，列的是 proto operation 名。
// HTTP transport 报的 operation 也是 proto 名；走内部凭证（X-Internal-Token），不走用户 JWT。
const (
	InternalOpCheckMembership         = "/api.conversation.v1.Conversation/CheckMembership"
	InternalOpUpdateUnread            = "/api.conversation.v1.Conversation/UpdateUnread"
	InternalOpUpdateUnreadBatch       = "/api.conversation.v1.Conversation/UpdateUnreadBatch"
	InternalOpGetUnread               = "/api.conversation.v1.Conversation/GetUnread"
	InternalOpClearUnread             = "/api.conversation.v1.Conversation/ClearUnread"
	InternalOpListUnread              = "/api.conversation.v1.Conversation/ListUnread"
	InternalOpListConversationMembers = "/api.conversation.v1.Conversation/ListConversationMembers"
	InternalOpListUserConversations   = "/api.conversation.v1.Conversation/ListUserConversations"
)

// InternalOps 该域全部内部端点列表。
func InternalOps() []string {
	return []string{
		InternalOpCheckMembership,
		InternalOpUpdateUnread,
		InternalOpUpdateUnreadBatch,
		InternalOpGetUnread,
		InternalOpClearUnread,
		InternalOpListUnread,
		InternalOpListConversationMembers,
		InternalOpListUserConversations,
	}
}

// authMiddlewares 返回鉴权中间件：用户 JWT 与内部凭证。
// ac 鉴权配置
// store token 黑名单查询器
// 白名单（免 JWT）由 selector 的 Match 圈定 —— selector 是「匹配上才执行」，这里取反；白名单是 auth.skip_operations 加本域内部端点。
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

// ProviderSet server 层的 providers。
var ProviderSet = wire.NewSet(NewGRPCServer, NewHTTPServer)
