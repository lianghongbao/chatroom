//go:build wireinject
// +build wireinject

// build tag 保证这个 stub 不进最终产物（真正生效的是 wire_gen.go）。

package main

import (
	"log/slog"

	"chatroom/app/friend/service/internal/biz"
	"chatroom/app/friend/service/internal/conf"
	"chatroom/app/friend/service/internal/data"
	obs "chatroom/app/friend/service/internal/observability"
	"chatroom/app/friend/service/internal/server"
	"chatroom/app/friend/service/internal/service"
	"chatroom/pkg/middleware"
	"chatroom/pkg/nacos"
	pkgobs "chatroom/pkg/observability"

	"github.com/go-kratos/kratos/v3"
	"github.com/google/wire"
)

// wireApp 组装 kratos 应用。
func wireApp(*conf.Bootstrap, *conf.Registry, *slog.Logger) (*kratos.App, func(), error) {
	panic(wire.Build(
		// 本域的 server 层：构造 HTTP/gRPC server 并注册 friend 域的路由。
		server.ProviderSet,
		middleware.NewBlacklistChecker,
		data.ProviderSet,
		biz.ProviderSet,
		service.ProviderSet,
		// nacos：注册本实例 + 发现下游服务。
		nacos.NewNamingClient,
		nacos.NewRegistrar,
		nacos.NewDiscovery,
		// 把本服务的 registry.nacos 段翻译成 pkg/nacos.Options
		conf.NacosOptions,
		// 把 *conf.Bootstrap 的子字段暴露为独立 provider。
		wire.FieldsOf(new(*conf.Bootstrap), "Server", "Data", "Auth", "Observability"),
		// 观测能力：conf.Observability → observability.Config → *observability.Provider。
		obs.NewConfig,
		pkgobs.NewPromRegistry,
		pkgobs.New,
		// server 中间件需要 middleware.TokenChecker（查 access token 黑名单）。
		wire.Bind(new(middleware.TokenChecker), new(*middleware.BlacklistChecker)),
		// 把 data.RemoteUserClient 折成 biz.FriendProfileFetcher。
		wire.Bind(new(biz.FriendProfileFetcher), new(*data.FriendAdapter)),
		newApp,
	))
}
