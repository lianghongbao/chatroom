package biz

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"strings"

	"github.com/go-kratos/kratos/v3/transport"
	kratostransporthttp "github.com/go-kratos/kratos/v3/transport/http"
)

// newJTI 生成 32 位 hex 字符串，用 crypto/rand 保证安全场景可用
func newJTI() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// userAgentFromContext 从 transport ctx 取 User-Agent，读不到时返 "unknown"
func userAgentFromContext(ctx context.Context) string {
	tr, ok := transport.FromServerContext(ctx)
	if !ok {
		return "unknown"
	}
	ua := tr.RequestHeader().Get("User-Agent")
	if ua == "" {
		return "unknown"
	}
	return ua
}

// ClientIPFromContext 从 transport ctx 取调用方 IP，读不到时返 "unknown"。
//
// 优先取 X-Forwarded-For 的第一个地址（服务在网关 / 负载均衡后面时，
// RemoteAddr 是它们的 IP，不是真实客户端的）。没有就走 X-Real-IP，
// 再没有就用 HTTP 请求的 RemoteAddr 的 host 部分。
func ClientIPFromContext(ctx context.Context) string {
	tr, ok := transport.FromServerContext(ctx)
	if !ok {
		return "unknown"
	}
	h := tr.RequestHeader()
	if xff := h.Get("X-Forwarded-For"); xff != "" {
		// XFF 是 "客户端, 代理1, 代理2" —— 第一个才是真实客户端。
		if i := strings.IndexByte(xff, ','); i >= 0 {
			xff = xff[:i]
		}
		if ip := strings.TrimSpace(xff); ip != "" {
			return ip
		}
	}
	if rip := strings.TrimSpace(h.Get("X-Real-IP")); rip != "" {
		return rip
	}
	// HTTP transport 才能拿到 RemoteAddr（gRPC 那边只有 metadata）。
	if ht, ok := tr.(kratostransporthttp.Transporter); ok {
		if req := ht.Request(); req != nil {
			if host, _, err := net.SplitHostPort(req.RemoteAddr); err == nil && host != "" {
				return host
			}
			if req.RemoteAddr != "" {
				return req.RemoteAddr
			}
		}
	}
	return "unknown"
}
