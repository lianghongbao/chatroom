// Package observability 提供观测能力：OTLP trace / Prometheus metrics 初始化与 shutdown。
// New 必须在建 server 之前调用，否则 otel.GetTracerProvider 返 noop。
package observability

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"

	"github.com/go-kratos/kratos/v3/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	otelmw "github.com/go-kratos/kratos/contrib/otel/v3/metrics"
	oteltrace "github.com/go-kratos/kratos/contrib/otel/v3/tracing"
	"github.com/go-kratos/kratos/v3/middleware"
	"go.opentelemetry.io/otel"
)

// Config 可观测性配置。各服务从自己的 internal/conf 翻译出这个结构。
type Config struct {
	MetricsAddr     string // Prometheus exporter 的 HTTP 监听端点。空 = 不启动
	TracingEndpoint string // OTLP gRPC endpoint。空 = 不启动
	ServiceName     string // 服务名，trace resource 的 service.name 标签
	// UnauthSampleRate 未鉴权请求（401）的 trace 采样率，0~1。默认 0.1。
	UnauthSampleRate float64
}

// Shutdown flush OTEL 数据并关掉 exporter。未初始化时是 no-op。
type Shutdown func(context.Context) error

func noopShutdown(context.Context) error { return nil }

// Provider 持有已装配好的观测能力。
type Provider struct {
	cfg     *Config
	promReg *prometheus.Registry
	cleanup func()
}

// Cleanup 关掉 exporter 并 flush 未导出的数据。
func (p *Provider) Cleanup() {
	if p == nil || p.cleanup == nil {
		return
	}
	p.cleanup()
}

// NewPromRegistry 返回独立 prometheus.Registry，otelprom 落这里。
func NewPromRegistry() *prometheus.Registry {
	return prometheus.NewRegistry()
}

// New 装 trace / metrics exporter 并注册到 otel 全局 provider。
// cfg 是观测配置，promReg 是 Prometheus registry。
func New(cfg *Config, promReg *prometheus.Registry) (*Provider, error) {
	if cfg == nil {
		return &Provider{cleanup: func() {}}, nil
	}
	svcName := serviceName(cfg)

	res, rerr := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(svcName),
		),
	)
	if rerr != nil {
		return nil, rerr
	}

	var (
		closers       []func(context.Context) error
		meterProvider *metric.MeterProvider
	)

	if cfg.TracingEndpoint != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		exp, err := otlptracegrpc.New(ctx,
			otlptracegrpc.WithEndpoint(cfg.TracingEndpoint),
			otlptracegrpc.WithInsecure(),
		)
		if err != nil {
			return nil, err
		}
		// 未鉴权请求（401）的 span 按采样率丢弃。
		filtered := &filteringExporter{inner: exp, rate: unauthSampleRate(cfg)}
		bsp := sdktrace.NewBatchSpanProcessor(filtered,
			sdktrace.WithMaxQueueSize(2048),
			sdktrace.WithMaxExportBatchSize(512),
			sdktrace.WithBatchTimeout(5*time.Second),
		)
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithResource(res),
			sdktrace.WithSpanProcessor(bsp),
		)
		otel.SetTracerProvider(tp)
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{}, propagation.Baggage{},
		))
		closers = append(closers,
			func(ctx context.Context) error { return tp.Shutdown(ctx) },
		)
		log.Info("observability: tracing OTLP exporter enabled",
			"endpoint", cfg.TracingEndpoint, "unauth_sample_rate", unauthSampleRate(cfg))
	}

	if cfg.MetricsAddr != "" {
		if promReg == nil {
			return nil, errors.New("observability: metrics_addr set 但 prometheus registry 未注入")
		}
		exp, err := otelprom.New(otelprom.WithRegisterer(promReg))
		if err != nil {
			return nil, err
		}
		meterProvider = metric.NewMeterProvider(
			metric.WithReader(exp),
			metric.WithResource(res),
		)
		otel.SetMeterProvider(meterProvider)
		closers = append(closers, meterProvider.Shutdown)

		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.HandlerFor(promReg, promhttp.HandlerOpts{Registry: promReg}))

		ln, err := net.Listen("tcp", cfg.MetricsAddr)
		if err != nil {
			return nil, err
		}
		srv := &http.Server{
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		}
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("metrics http serve failed", "err", err)
			}
		}()
		closers = append(closers, srv.Shutdown)
		log.Info("observability: metrics prometheus exporter enabled", "addr", cfg.MetricsAddr)
	}

	shutdown := shutdownOf(closers)
	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdown(ctx)
	}
	return &Provider{cfg: cfg, promReg: promReg, cleanup: cleanup}, nil
}

// ClientMiddleware 返回出站调用的 tracing 中间件。tracing 没开时返 nil。
func (p *Provider) ClientMiddleware() middleware.Middleware {
	if p == nil || p.cfg == nil || p.cfg.TracingEndpoint == "" {
		return nil
	}
	return oteltrace.Client(
		oteltrace.WithTracerProvider(otel.GetTracerProvider()),
		oteltrace.WithPropagator(otel.GetTextMapPropagator()),
		oteltrace.WithTracerName(serviceName(p.cfg)),
	)
}

// Middlewares 返回 server-side middleware 列表。必须在 New 之后调用。
func (p *Provider) Middlewares() []middleware.Middleware {
	if p == nil || p.cfg == nil {
		return nil
	}
	cfg := p.cfg
	var mws []middleware.Middleware
	name := serviceName(cfg)

	if cfg.TracingEndpoint != "" {
		mws = append(mws, oteltrace.Server(
			oteltrace.WithTracerProvider(otel.GetTracerProvider()),
			oteltrace.WithPropagator(otel.GetTextMapPropagator()),
			oteltrace.WithTracerName(name),
		))
	}
	if cfg.MetricsAddr != "" {
		meter := otel.Meter(name)
		requests, rerr := otelmw.DefaultRequestsCounter(meter, "server_requests_code_total")
		seconds, serr := otelmw.DefaultSecondsHistogram(meter, "server_requests_seconds")
		if rerr == nil && serr == nil {
			mws = append(mws, otelmw.Server(
				otelmw.WithRequests(requests),
				otelmw.WithSeconds(seconds),
			))
		}
	}
	return mws
}

func shutdownOf(closers []func(context.Context) error) Shutdown {
	return func(ctx context.Context) error {
		var firstErr error
		for _, c := range closers {
			if c == nil {
				continue
			}
			if err := c(ctx); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	}
}

// defaultServiceName 是配置里没写 service_name 时的兜底值。
const defaultServiceName = "chatroom-unknown"

// serviceName 取 service.name 标签值。cfg 是观测配置。
func serviceName(cfg *Config) string {
	if cfg.ServiceName != "" {
		return cfg.ServiceName
	}
	return defaultServiceName
}

// defaultUnauthSampleRate 未鉴权请求的默认采样率。
const defaultUnauthSampleRate = 0.1

// unauthSampleRate 取未鉴权请求的采样率，夹在 0~1。cfg 是观测配置。
func unauthSampleRate(cfg *Config) float64 {
	if cfg == nil || cfg.UnauthSampleRate <= 0 {
		return defaultUnauthSampleRate
	}
	if cfg.UnauthSampleRate > 1 {
		return 1
	}
	return cfg.UnauthSampleRate
}
