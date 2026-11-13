// Package observability 把本服务的 conf 段翻译成 pkg/observability 的 Config。
// 翻译放在服务这边，避免 pkg/observability 反向依赖具体服务的 conf proto。
package observability

import (
	"chatroom/app/message/service/internal/conf"
	"chatroom/pkg/observability"
)

// NewConfig 把 conf.Observability 翻译成 observability.Config。
// o 观测配置段
func NewConfig(o *conf.Observability) *observability.Config {
	if o == nil {
		return nil
	}
	return &observability.Config{
		MetricsAddr:      o.GetMetricsAddr(),
		TracingEndpoint:  o.GetTracingEndpoint(),
		ServiceName:      o.GetServiceName(),
		UnauthSampleRate: o.GetUnauthSampleRate(),
	}
}
