// Package observability 把本服务的 conf 段翻译成 pkg/observability 的 Config。
//
// 翻译放在服务这边而不是 pkg 里：pkg/observability 不该认识任何服务的
// conf proto（那会让共享库反向依赖具体服务）。conf proto 本来就是每服务一份，
// 翻译跟着它走是对的。
package observability

import (
	"chatroom/app/message/service/internal/conf"
	"chatroom/pkg/observability"
)

// NewConfig 把 conf.Observability 翻译成 observability.Config。
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
