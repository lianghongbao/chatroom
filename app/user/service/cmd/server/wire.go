//go:build wireinject
// +build wireinject

// build tag 保证这个 stub 不进最终产物（真正生效的是 wire_gen.go）。

package main

import (
	"log/slog"

	"chatroom/app/user/service/internal/biz"
	"chatroom/app/user/service/internal/conf"
	"chatroom/app/user/service/internal/data"
	"chatroom/app/user/service/internal/observability"
	"chatroom/app/user/service/internal/server"
	"chatroom/app/user/service/internal/service"
	"chatroom/app/user/service/internal/storage"
	"chatroom/pkg/middleware"
	"chatroom/pkg/nacos"
	pkgobs "chatroom/pkg/observability"

	"github.com/go-kratos/kratos/v3"
	"github.com/google/wire"
)

// wireApp 组装 kratos 应用。入参依次是引导配置、registry 配置段与 logger。
func wireApp(*conf.Bootstrap, *conf.Registry, *slog.Logger) (*kratos.App, func(), error) {
	panic(wire.Build(
		// 本域的 server 层：构造 HTTP/gRPC server 并注册 user 域的路由
		server.ProviderSet,
		data.ProviderSet,
		biz.ProviderSet,
		service.ProviderSet,
		// nacos：注册本实例 + 发现下游服务。
		// 三个 provider 共用同一个 naming client，否则两边状态会不一致。
		nacos.NewNamingClient,
		nacos.NewRegistrar,
		// 把本服务的 registry.nacos 段翻译成 pkg/nacos.Options。
		conf.NacosOptions,
		// 把 *conf.Bootstrap 的子字段暴露为独立 provider，各层直接吃 *conf.Xxx，不做二次翻译
		wire.FieldsOf(new(*conf.Bootstrap), "Server", "Data", "Auth", "Observability", "Storage", "RateLimits"),
		// 观测能力：conf.Observability → observability.Config → *observability.Provider。
		// Provider 构造时即注册 otel 全局 provider，server 依赖 *Provider，
		// 依赖关系保证了它先于 server 建好。
		observability.NewConfig,
		pkgobs.NewPromRegistry,
		pkgobs.New,
		// 对象存储：没配就返回 nil client，头像上传能力自动关闭，
		// 不影响服务的其他功能。
		storage.NewClient,
		biz.NewStorageOptions,
		// *data.TokenStore 同时实现 biz.TokenStore 和 middleware.TokenChecker，显式绑定让 wire 拉到正确的窄接口
		wire.Bind(new(biz.TokenStore), new(*data.TokenStore)),
		wire.Bind(new(middleware.TokenChecker), new(*data.TokenStore)),
		// data.UserCache 实现 biz.UserCache。
		wire.Bind(new(biz.UserCache), new(*data.UserCache)),
		// data.Locker 实现 biz.Locker。
		wire.Bind(new(biz.Locker), new(*data.Locker)),
		newApp,
	))
}
