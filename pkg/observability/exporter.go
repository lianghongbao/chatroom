package observability

import (
	"context"
	"hash/fnv"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// authDeniedKey 未鉴权请求在 span 上打的标记。
const authDeniedKey = "chatroom.auth.denied"

// MarkAuthDenied 给当前 span 打上「未鉴权」标记。没有 span 时是 no-op。
func MarkAuthDenied(ctx context.Context) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	span.SetAttributes(attribute.Bool(authDeniedKey, true))
}

// filteringExporter 包一层 SpanExporter，按采样率丢弃「未鉴权」的 span。
type filteringExporter struct {
	inner sdktrace.SpanExporter
	// rate 未鉴权 span 的保留比例，0~1。
	rate float64
}

// ExportSpans 过滤后交给内层导出器。
func (e *filteringExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	kept := make([]sdktrace.ReadOnlySpan, 0, len(spans))
	for _, s := range spans {
		if e.isAuthDenied(s) && !e.sample(s) {
			continue
		}
		kept = append(kept, s)
	}
	if len(kept) == 0 {
		return nil
	}
	return e.inner.ExportSpans(ctx, kept)
}

// Shutdown 关掉内层导出器。
func (e *filteringExporter) Shutdown(ctx context.Context) error {
	return e.inner.Shutdown(ctx)
}

// isAuthDenied span 是否带「未鉴权」标记。
func (e *filteringExporter) isAuthDenied(s sdktrace.ReadOnlySpan) bool {
	for _, attr := range s.Attributes() {
		if string(attr.Key) == authDeniedKey {
			return attr.Value.AsBool()
		}
	}
	return false
}

// sample 按 trace id 哈希决定是否保留，同一个 trace 的所有 span 判定一致。
func (e *filteringExporter) sample(s sdktrace.ReadOnlySpan) bool {
	if e.rate >= 1 {
		return true
	}
	if e.rate <= 0 {
		return false
	}
	h := fnv.New64a()
	tid := s.SpanContext().TraceID()
	_, _ = h.Write(tid[:])
	// 取哈希的高 53 位，比率 × 2^53 得到阈值。
	threshold := uint64(e.rate * float64(1<<53))
	return (h.Sum64() >> 11) < threshold
}
