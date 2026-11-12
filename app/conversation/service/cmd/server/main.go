package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"chatroom/app/conversation/service/internal/conf"
	"chatroom/pkg/nacos"
	"chatroom/pkg/observability"

	"github.com/go-kratos/kratos/contrib/otel/v3/tracing"
	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/config"
	"github.com/go-kratos/kratos/v3/config/env"
	"github.com/go-kratos/kratos/v3/config/file"
	"github.com/go-kratos/kratos/v3/log"
	"github.com/go-kratos/kratos/v3/registry"
	"github.com/go-kratos/kratos/v3/transport/grpc"
	"github.com/go-kratos/kratos/v3/transport/http"

	_ "go.uber.org/automaxprocs"
)

// go build -ldflags "-X main.Version=x.y.z"
var (
	// Name 服务名。
	Name string
	// Version 服务版本号。
	Version string
	// flagconf 业务配置文件路径。
	flagconf string
	// flagbootstrap 引导配置文件路径。
	flagbootstrap string

	id, _ = os.Hostname()
)

func init() {
	// 指向具体文件而非目录，避免 Kratos 把 README.md 等其它文件当配置加载。
	flag.StringVar(&flagconf, "conf", "configs/config.yaml", "config path, eg: -conf config.yaml")
	// bootstrap 只回答「连哪个 nacos、从哪条 dataId 取配置」。
	flag.StringVar(&flagbootstrap, "bootstrap", "configs/bootstrap.yaml", "bootstrap config path, eg: -bootstrap bootstrap.yaml")
}

func newApp(
	logger *slog.Logger,
	gs *grpc.Server,
	hs *http.Server,
	reg registry.Registrar, // 用于 kratos.Registrar(reg)
	regCfg *conf.Registry, // 用于读 ServiceName
	obs *observability.Provider, // 用于关掉 exporter
) *kratos.App {
	// ServiceName 优先级：配置覆盖 build flag。
	name := Name
	if nc := regCfg.GetNacos(); nc != nil && nc.GetServiceName() != "" {
		name = nc.GetServiceName()
	}

	opts := []kratos.Option{
		kratos.ID(id),
		kratos.Name(name),
		kratos.Version(Version),
		kratos.Metadata(map[string]string{}),
		kratos.Logger(logger),
		kratos.Server(gs, hs),
	}
	// 注册到 nacos。reg 为 nil 时 kratos 内部已做检查，不会启动注册循环。
	if reg != nil {
		opts = append(opts, kratos.Registrar(reg))
	}
	// 服务停掉后 flush OTEL 数据并关 exporter，避免最后一批 span 丢在内存里。
	if obs != nil {
		opts = append(opts, kratos.AfterStop(func(context.Context) error {
			obs.Cleanup()
			return nil
		}))
	}

	return kratos.New(opts...)
}

func main() {
	flag.Parse()
	// 第一阶段：base logger 不绑定 service.* 属性，先 set 为全局默认。
	base := log.NewLogger(
		slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
			AddSource: true,
			Level:     slog.LevelInfo,
		}),
		log.WithExtractor(tracing.TraceAttrs),
	)
	log.SetDefault(base)

	// 第一阶段：只用 bootstrap.yaml + env 加载一份引导配置，从中取出
	// registry.nacos 段。这一步刻意不读业务配置 —— 业务配置在 nacos 上，
	// 而「连哪个 nacos」必须在连上之前就知道。两份文件拆开正是为了断掉这个循环依赖。
	bootstrapOnly := config.New(
		config.WithSource(
			file.NewSource(flagbootstrap),
			// 不传 prefix：让 ${NACOS_USERNAME} 这种 yaml 占位符能直接从 env map 查到。
			env.NewSource(),
		),
	)
	if err := bootstrapOnly.Load(); err != nil {
		panic(err)
	}
	var bootstrap conf.Bootstrap
	if err := bootstrapOnly.Scan(&bootstrap); err != nil {
		panic(err)
	}
	bootstrapOnly.Close()

	opts := conf.NacosOptions(bootstrap.Registry)

	// 第二阶段：构造完整 config。优先级从低到高：file < nacos < env。
	// Kratos 的合并语义是后注册的源覆盖前面的，所以注册顺序 file → nacos → env。
	// file 源用 flagconf（业务配置）而非 flagbootstrap —— bootstrap 段只用来定位
	// nacos，不参与业务配置合并，否则 nacos 上的旧值永远盖不掉本地的 registry 段。
	var sources []config.Source
	sources = append(sources, file.NewSource(flagconf))
	if len(opts.ServerAddresses) > 0 {
		nacosClient, err := nacos.NewConfigClient(opts)
		if err != nil {
			panic(err)
		}
		sources = append(sources, nacos.NewSource(nacosClient,
			nacos.WithDataID(opts.DataIDOrDefault()),
			nacos.WithGroup(opts.GroupOrDefault()),
		))
	}
	sources = append(sources, env.NewSource())

	c := config.New(config.WithSource(sources...))
	defer c.Close()

	if err := c.Load(); err != nil {
		panic(err)
	}

	var bc conf.Bootstrap
	if err := c.Scan(&bc); err != nil {
		panic(err)
	}

	// 打印关键字段，便于核对值是否生效。
	log.Info(fmt.Sprintf("merged config: server.http.timeout=%s data.redis.read_timeout=%s db.source=%q redis.addr=%q",
		bc.GetServer().GetHttp().GetTimeout(),
		bc.GetData().GetRedis().GetReadTimeout(),
		bc.GetData().GetDatabase().GetSource(),
		bc.GetData().GetRedis().GetAddr(),
	))

	// 解析最终 service.name（配置覆盖 build flag），构造带属性的 final logger。
	name := Name
	if nc := bootstrap.Registry.GetNacos(); nc != nil && nc.GetServiceName() != "" {
		name = nc.GetServiceName()
	}
	logger := base.With(
		slog.String("service.id", id),
		slog.String("service.name", name),
		slog.String("service.version", Version),
	)
	log.SetDefault(logger)

	app, cleanup, err := wireApp(&bc, bootstrap.Registry, logger)
	if err != nil {
		panic(err)
	}
	defer cleanup()

	if err := app.Run(); err != nil {
		panic(err)
	}
}
