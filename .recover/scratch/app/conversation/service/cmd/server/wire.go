//go:build wireinject
// +build wireinject

// build tag 保证这个 stub 不进最终产物（真正生效的是 wire_gen.go）。

package main

import (
	"log/slog"

	"chatroom/conversation-service/internal/biz"
	"chatroom/conversation-service/internal/conf"
	"chatroom/conversation-service/internal/data"
	obs "chatroom/conversation-service/internal/observability"
	"chatroom/conversation-service/internal/server"
	"chatroom/conversation-service/internal/service"
	"chatroom/pkg/middleware"
	"chatroom/pkg/nacos"
	pkgobs "chatroom/pkg/observability"

	"github.com/go-kratos/kratos/v3"
	"github.com/google/wire"
)

// wireApp 组装 kratos 应用。
func wireApp(*conf.Bootstrap, *conf.Registry, *slog.Logger) (*kratos.App, func(), error) {
	panic(wire.Build(
		// 本域的 server 层：构造 HTTP/gRPC server 并注册 conversation 域的路由。
		server.ProviderSet,
		middleware.NewBlacklistChecker,
		data.ProviderSet,
		biz.ProviderSet,
		service.ProviderSet,
		// nacos：注册本实例 + 发现下游服务。
		// 三个 provider 共用同一个 naming client（pkg/nacos.NewNamingClient 产出的
		// 那一个）—— 注册和发现必须是同一个 client，否则两边状态会不一致。
		nacos.NewNamingClient,
		nacos.NewRegistrar,
		nacos.NewDiscovery,
		// 把本服务的 registry.nacos 段翻译成 pkg/nacos.Options
		conf.NacosOptions,
		// 把 *conf.Bootstrap 的子字段暴露为独立 provider，
		// 让各层的构造函数直接吃 *conf.Xxx，不做二次翻译。
		wire.FieldsOf(new(*conf.Bootstrap), "Server", "Data", "Auth", "Observability"),
		// 观测能力：conf.Observability → observability.Config → *observability.Provider。
		// Provider 构造时即注册 otel 全局 provider，所以 wire 会在建 server 之前先建它 ——
		// server 依赖 *Provider，依赖关系保证了顺序。
		obs.NewConfig,
		pkgobs.NewPromRegistry,
		pkgobs.New,
		// server 中间件需要 middleware.TokenChecker（查 access token 黑名单）。
		wire.Bind(new(middleware.TokenChecker), new(*middleware.BlacklistChecker)),
		// 把 data.RemoteUserClient 折成 biz.ConversationProfileFetcher。
		wire.Bind(new(biz.ConversationProfileFetcher), new(*data.ConversationAdapter)),
		// 把 data.RemoteUserClient 折成 biz.UserExistenceChecker。
		wire.Bind(new(biz.UserExistenceChecker), new(*data.ConversationAdapter)),
		// 把 data.MemberInvalidationPublisher 折成 biz.MemberInvalidationPublisher。
		wire.Bind(new(biz.MemberInvalidationPublisher), new(*data.MemberInvalidationPublisher)),
		newApp,
	))
}
