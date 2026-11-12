// Package middleware 提供鉴权中间件：JWT 鉴权、内部凭证校验、access token 黑名单查询。
package middleware

import (
	"context"
	"crypto/subtle"
	"strings"
	"time"

	commerrors "chatroom/pkg/errors"
	"chatroom/pkg/observability"

	"github.com/go-kratos/kratos/v3/errors"
	"github.com/go-kratos/kratos/v3/log"
	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/transport"
	jwtv5 "github.com/golang-jwt/jwt/v5"
)

const (
	reasonBadToken  = "INVALID_TOKEN"
	defaultIssuer   = "chatroom"
	tokenTypeAccess = "access"
)

// 凭证无效的错误原因
var reasonUnauthorized = commerrors.Reason(commerrors.ErrorReasonCommonUnauthenticated)

// 内部凭证失败的错误原因
var reasonInternalForbidden = commerrors.Reason(commerrors.ErrorReasonCommonInternalTokenInvalid)

// TokenChecker 中间件所需的最小接口，data.TokenStore 隐式满足
type TokenChecker interface {
	IsBlacklisted(ctx context.Context, jti string) (bool, error)
}

// Option 是 JWT 中间件的配置项。
type Option func(*options)

// options 中间件的可配置项。
type options struct {
	signingMethod jwtv5.SigningMethod
	parserOptions []jwtv5.ParserOption
	// blacklistedReason token 已被拉黑时的错误原因。
	blacklistedReason string
}

// WithSigningMethod 指定签名算法，默认 HS256。
func WithSigningMethod(method jwtv5.SigningMethod) Option {
	return func(o *options) {
		o.signingMethod = method
	}
}

// WithParserOptions 透传给 jwt.Parser，用于强制校验 iss/sub/exp 等 claim。
func WithParserOptions(opts ...jwtv5.ParserOption) Option {
	return func(o *options) {
		o.parserOptions = opts
	}
}

// WithBlacklistedReason 指定「token 已被拉黑」的错误原因。
func WithBlacklistedReason(reason string) Option {
	return func(o *options) {
		if reason != "" {
			o.blacklistedReason = reason
		}
	}
}

// AuthInfo 中间件写入 ctx 的鉴权信息。
type AuthInfo struct {
	UID string // 用户标识
	JTI string // token 唯一标识，登出时用于定位黑名单条目
	Exp time.Time
}

// Server 返回 JWT 鉴权中间件。流程：parse → verify → 黑名单 → 写 ctx。
// 生效范围由调用方用 selector 圈定。
func Server(secret, issuer string, store TokenChecker, opts ...Option) middleware.Middleware {
	o := &options{signingMethod: jwtv5.SigningMethodHS256}
	for _, opt := range opts {
		opt(o)
	}
	if issuer == "" {
		issuer = defaultIssuer
	}
	return func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			tr, ok := transport.FromServerContext(ctx)
			if !ok {
				// 非 transport ctx 场景（如直调）放行
				return next(ctx, req)
			}
			raw := bearerToken(tr.RequestHeader().Get("Authorization"))
			if raw == "" {
				observability.MarkAuthDenied(ctx)
				return nil, errors.Unauthorized(reasonUnauthorized, "缺少 Bearer token")
			}
			claims, err := verifyAccess(raw, secret, issuer, o.signingMethod, o.parserOptions)
			if err != nil {
				observability.MarkAuthDenied(ctx)
				return nil, errors.Unauthorized(reasonBadToken, err.Error())
			}

			// 黑名单降级放行；只有确认在黑名单才拒绝
			if store != nil {
				blacklisted, err := store.IsBlacklisted(ctx, claims.JTI)
				if err != nil {
					log.WarnContext(ctx, "黑名单检查降级", "err", err)
				} else if blacklisted {
					return nil, errors.Unauthorized(o.blacklistedReason, "token 已被登出")
				}
			}

			return next(NewContext(ctx, *claims), req)
		}
	}
}

// InternalTokenHeader 内部凭证的请求头名
const InternalTokenHeader = "X-Internal-Token"

// InternalServer 校验服务间内部凭证（X-Internal-Token 头）。
// secret 为空时受管辖的端点一律 403。
// 管辖范围由调用方用 selector 圈定。
func InternalServer(secret string) middleware.Middleware {
	return func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			tr, ok := transport.FromServerContext(ctx)
			if !ok {
				// 非 transport ctx 场景（如直调）不受管辖，放行
				return next(ctx, req)
			}
			if secret == "" {
				// 未配置凭证 → fail closed
				return nil, errors.Forbidden(reasonInternalForbidden, "internal endpoint is disabled")
			}
			raw := strings.TrimSpace(tr.RequestHeader().Get(InternalTokenHeader))
			if raw == "" {
				return nil, errors.Forbidden(reasonInternalForbidden, "missing internal token")
			}
			// 常量时间比较，避免通过响应时间侧信道逐字节猜凭证
			if subtle.ConstantTimeCompare([]byte(raw), []byte(secret)) != 1 {
				return nil, errors.Forbidden(reasonInternalForbidden, "invalid internal token")
			}
			return next(ctx, req)
		}
	}
}

// OpInList 判断 operation 是否命中列表。精确匹配或前缀匹配。
func OpInList(operation string, list []string) bool {
	for _, s := range list {
		if s == "" {
			continue
		}
		if operation == s || strings.HasPrefix(operation, s) {
			return true
		}
	}
	return false
}

// bearerToken 提取 Bearer token，大小写不敏感。
func bearerToken(h string) string {
	const prefix = "bearer "
	if len(h) <= len(prefix) {
		return ""
	}
	if !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// authKey 是 ctx 的 key 类型。
type authKey struct{}

// NewContext 把鉴权信息写入 ctx。
func NewContext(ctx context.Context, info AuthInfo) context.Context {
	return context.WithValue(ctx, authKey{}, info)
}

// FromContext 从 ctx 取鉴权 uid；未鉴权返 ("", false)。
func FromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(authKey{}).(AuthInfo)
	return v.UID, ok && v.UID != ""
}

// JTIFromContext 从 ctx 取 access token 的 jti。Logout 用以定位要拉黑的 token。
func JTIFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(authKey{}).(AuthInfo)
	return v.JTI, ok && v.JTI != ""
}

// ExpFromContext 从 ctx 取 access token 的过期时间。
func ExpFromContext(ctx context.Context) (time.Time, bool) {
	v, ok := ctx.Value(authKey{}).(AuthInfo)
	return v.Exp, ok && !v.Exp.IsZero()
}

// VerifyAccessToken 校验 access token 返 (uid, jti, exp)。不查黑名单。
func VerifyAccessToken(raw, secret, issuer string) (uid string, jti string, exp time.Time, err error) {
	claims, err := verifyAccess(raw, secret, issuer, jwtv5.SigningMethodHS256, nil)
	if err != nil {
		return "", "", time.Time{}, err
	}
	return claims.UID, claims.JTI, claims.Exp, nil
}

// ParseRefreshToken 解析 refresh token 返 (uid, jti, exp)。
func ParseRefreshToken(raw, secret, issuer string) (uid string, jti string, exp time.Time, err error) {
	return parseRefresh(raw, secret, issuer)
}

func parseRefresh(raw, secret, issuer string) (string, string, time.Time, error) {
	if issuer == "" {
		issuer = defaultIssuer
	}
	tok, err := jwtv5.Parse(raw, func(t *jwtv5.Token) (any, error) {
		if t.Method.Alg() != jwtv5.SigningMethodHS256.Alg() {
			return nil, jwtv5.ErrTokenSignatureInvalid
		}
		return []byte(secret), nil
	})
	if err != nil || !tok.Valid {
		return "", "", time.Time{}, err
	}
	claims, ok := tok.Claims.(jwtv5.MapClaims)
	if !ok {
		return "", "", time.Time{}, jwtv5.ErrTokenInvalidClaims
	}
	if iss, _ := claims["iss"].(string); iss != issuer {
		return "", "", time.Time{}, jwtv5.ErrTokenInvalidIssuer
	}
	if typ, _ := claims["typ"].(string); typ != "refresh" {
		return "", "", time.Time{}, jwtv5.ErrTokenInvalidClaims
	}
	jti, _ := claims["jti"].(string)
	if jti == "" {
		return "", "", time.Time{}, jwtv5.ErrTokenInvalidClaims
	}
	expFloat, ok := claims["exp"].(float64)
	if !ok {
		return "", "", time.Time{}, jwtv5.ErrTokenInvalidClaims
	}
	exp := time.Unix(int64(expFloat), 0)
	if time.Now().After(exp) {
		return "", "", time.Time{}, jwtv5.ErrTokenExpired
	}
	uid, err := claimToUID(claims["sub"])
	if err != nil || uid == "" {
		return "", "", time.Time{}, jwtv5.ErrTokenInvalidClaims
	}
	return uid, jti, exp, nil
}

// verifyAccess 校验 access token：iss / typ / jti / exp / sub。
func verifyAccess(raw, secret, issuer string, method jwtv5.SigningMethod, parserOpts []jwtv5.ParserOption) (*AuthInfo, error) {
	tok, err := jwtv5.Parse(raw, func(t *jwtv5.Token) (any, error) {
		if t.Method.Alg() != method.Alg() {
			return nil, jwtv5.ErrTokenSignatureInvalid
		}
		return []byte(secret), nil
	}, parserOpts...)
	if err != nil || !tok.Valid {
		return nil, err
	}
	claims, ok := tok.Claims.(jwtv5.MapClaims)
	if !ok {
		return nil, jwtv5.ErrTokenInvalidClaims
	}
	if iss, _ := claims["iss"].(string); iss != issuer {
		return nil, jwtv5.ErrTokenInvalidIssuer
	}
	if typ, _ := claims["typ"].(string); typ != tokenTypeAccess {
		return nil, jwtv5.ErrTokenInvalidClaims
	}
	jti, _ := claims["jti"].(string)
	if jti == "" {
		return nil, jwtv5.ErrTokenInvalidClaims
	}
	expFloat, ok := claims["exp"].(float64)
	if !ok {
		return nil, jwtv5.ErrTokenInvalidClaims
	}
	exp := time.Unix(int64(expFloat), 0)
	if time.Now().After(exp) {
		return nil, jwtv5.ErrTokenExpired
	}
	uid, err := claimToUID(claims["sub"])
	if err != nil || uid == "" {
		return nil, jwtv5.ErrTokenInvalidClaims
	}
	return &AuthInfo{UID: uid, JTI: jti, Exp: exp}, nil
}

// claimToUID 从 sub claim 取 uid。
func claimToUID(v any) (string, error) {
	s, ok := v.(string)
	if !ok || s == "" {
		return "", jwtv5.ErrTokenInvalidClaims
	}
	return s, nil
}
