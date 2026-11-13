//go:build wireinject
// +build wireinject

package main

import (
	"log/slog"

	"chatroom/app/ws-gateway/service/internal/conf"
	"chatroom/app/ws-gateway/service/internal/data"
	"chatroom/app/ws-gateway/service/internal/gateway"
	obs "chatroom/app/ws-gateway/service/internal/observability"
	"chatroom/app/ws-gateway/service/internal/server"
	pkgobs "chatroom/pkg/observability"

	"chatroom/pkg/nacos"

	"github.com/go-kratos/kratos/v3"
	"github.com/google/wire"
)

// wireApp 组装 kratos 应用。
func wireApp(*conf.Bootstrap, *conf.Registry, *slog.Logger) (*kratos.App, func(), error) {
	panic(wire.Build(
		// 网关的 server 层：只构造 HTTP server（挂 WS 端点），没有 proto 服务
		server.NewHTTPServer,
		gateway.NewLifecycleWithInvalidator,
		data.ProviderSet,
		// 网关不需要业务用例层 —— 它只维护连接 + 转发。
		gateway.ProviderSet,
		// nacos：注册本实例 + 发现下游服务。
		// 三个 provider 共用同一个 naming client，否则两边状态会不一致。
		nacos.NewNamingClient,
		nacos.NewRegistrar,
		nacos.NewDiscovery,
		// 把本服务的 registry.nacos 段翻译成 pkg/nacos.Options。
		conf.NacosOptions,
		// 把 *conf.Bootstrap 的子字段暴露为独立 provider，让各层的构造函数
		// 能直接收到自己需要的 proto 段
		wire.FieldsOf(new(*conf.Bootstrap), "Server", "Data", "Auth", "Observability"),
		// 观测能力：conf.Observability → observability.Config → *observability.Provider。
		// Provider 构造时即注册 otel 全局 provider，server 依赖 *Provider，
		// 依赖关系保证了它先于 server 建好。
		obs.NewConfig,
		pkgobs.NewPromRegistry,
		pkgobs.New,
		// 把 data.RemoteConversationClient 折成 gateway.MemberFetcher ——
		// 算广播目标（会话成员列表）。
		wire.Bind(new(gateway.MemberFetcher), new(*data.MembersAdapter)),
		// 把 data.RemoteConversationClient 折成 gateway.UserConversationLister ——
		// 用户上下线时找「他在哪些会话里」。
		wire.Bind(new(gateway.UserConversationLister), new(*data.UserConversationListerAdapter)),
		// 把 data.RemoteMessageClient 折成 gateway.MessageSender ——
		// 转发聊天帧到 message 服务落库。
		wire.Bind(new(gateway.MessageSender), new(*data.Sender)),
		// 把 data.RemoteMessageClient 折成 gateway.MessageLister ——
		// 断线重连时补回漏掉的消息。
		wire.Bind(new(gateway.MessageLister), new(*data.Lister)),
		newApp,
	))
}
