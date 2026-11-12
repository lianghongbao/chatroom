// Package testutil 提供测试脚手架：临时数据库、测试 Redis、带鉴权上下文的请求 ctx。
package testutil

import (
	"context"
	"testing"
	"time"

	"chatroom/pkg/middleware"

	"github.com/go-kratos/kratos/v3/transport"
	jwtv5 "github.com/golang-jwt/jwt/v5"
)

// DefaultSecret 测试用的固定 JWT 密钥，长度 ≥ 32 字节（HS256 要求）。
const DefaultSecret = "test-secret-32-bytes-long-xxxxxx"

// FakeHeader 是 transport.Header 的最小实现，仅 Get 有实际返回。
type FakeHeader struct{ values map[string]string }

func (h *FakeHeader) Get(key string) string    { return h.values[key] }
func (h *FakeHeader) Set(k, v string)          { h.values[k] = v }
func (h *FakeHeader) Add(k, v string)          { h.values[k] = v }
func (h *FakeHeader) Keys() []string           { return nil }
func (h *FakeHeader) Values(k string) []string { return nil }

// FakeTransporter 是 transport.Transporter 的最小实现。
type FakeTransporter struct {
	header transport.Header
}

func (f *FakeTransporter) Kind() transport.Kind            { return transport.KindGRPC }
func (f *FakeTransporter) Endpoint() string                { return "" }
func (f *FakeTransporter) Operation() string               { return "/api.test.v1.Test/Get" }
func (f *FakeTransporter) RequestHeader() transport.Header { return f.header }
func (f *FakeTransporter) ReplyHeader() transport.Header   { return &FakeHeader{} }

// JWTFor 用给定密钥给 uid 签 HS256 token。
func JWTFor(t *testing.T, secret string, uid string) string {
	t.Helper()
	tok := jwtv5.NewWithClaims(jwtv5.SigningMethodHS256, jwtv5.MapClaims{
		"sub": uid,
		"jti": "test-jti",
		"typ": "access",
		"iss": "chatroom", // auth.Server(secret, "", nil) 走默认 issuer "chatroom"
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	s, err := tok.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return s
}

// AuthedCtxWithToken 走一次 auth.Server 中间件，让它把 uid 写到 ctx。
// bearer 是 Authorization 头的值（不含 "Bearer " 前缀）。
func AuthedCtxWithToken(t *testing.T, bearer, secret string) context.Context {
	t.Helper()
	hdr := &FakeHeader{values: map[string]string{"Authorization": "Bearer " + bearer}}
	tr := &FakeTransporter{header: hdr}
	ctx := transport.NewServerContext(context.Background(), tr)

	mw := middleware.Server(secret, "", nil) // nil store = 关闭黑名单检查
	type dummyReq struct{}
	var capturedCtx context.Context
	_, err := mw(func(ctx context.Context, _ any) (any, error) {
		capturedCtx = ctx
		return nil, nil
	})(ctx, &dummyReq{})
	if err != nil {
		t.Fatalf("auth middleware rejected test ctx: %v", err)
	}
	if capturedCtx == nil {
		t.Fatal("auth middleware did not invoke next")
	}
	if _, ok := middleware.FromContext(capturedCtx); !ok {
		t.Fatal("auth middleware did not write uid into ctx")
	}
	return capturedCtx
}

// AuthedCtx 给 uid 造一个带鉴权的请求 ctx（用 DefaultSecret）。
func AuthedCtx(t *testing.T, uid string) context.Context {
	t.Helper()
	return AuthedCtxWithToken(t, JWTFor(t, DefaultSecret, uid), DefaultSecret)
}
