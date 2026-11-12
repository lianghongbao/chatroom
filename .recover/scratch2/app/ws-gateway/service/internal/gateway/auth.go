package gateway

import (
	"context"
	"time"

	"chatroom/ws-gateway-service/internal/middleware"

	"github.com/redis/go-redis/v9"
)

// TokenVerifier 校验握手 token 返用户 uid。
type TokenVerifier interface {
	Verify(raw string) (string, error)
}

// TokenVerifierFunc 把一个函数适配成 TokenVerifier。
type TokenVerifierFunc func(raw string) (string, error)

// Verify 调 f。
func (f TokenVerifierFunc) Verify(raw string) (string, error) {
	return f(raw)
}

// NewJWTVerifier 构造基于 JWT 的 token 校验器（握手鉴权用）。
//
// issuer 为空时用 middleware 包里的默认值。
//
// blacklist 为 nil 时不查黑名单 —— 降级放行，跟其它服务一致
// （access token 自身的 TTL 是最终兜底）。
func NewJWTVerifier(secret, issuer string, blacklist middleware.TokenChecker) TokenVerifier {
	return TokenVerifierFunc(func(raw string) (string, error) {
		uid, jti, _, err := middleware.VerifyAccessToken(raw, secret, issuer)
		if err != nil {
			return "", err
		}
		// token 被登出（拉黑）后要拒掉 —— 否则用户登出了，
		// WS 长连接还能继续收发消息，转发给下游时 token 依然有效。
		if blacklist != nil {
			ctx, cancel := context.WithTimeout(context.Background(), blacklistTimeout)
			defer cancel()
			blacklisted, err := blacklist.IsBlacklisted(ctx, jti)
			if err != nil {
				// 查不了就放行 —— 跟其它服务的降级约定一致。
				// 宁可放过一个已登出的连接，也不能让 Redis 抖动把所有连接都拒了。
				return uid, nil
			}
			if blacklisted {
				return "", errTokenBlacklisted
			}
		}
		return uid, nil
	})
}

// blacklistTimeout 查黑名单的超时。
const blacklistTimeout = 2 * time.Second

// errTokenBlacklisted token 已被登出。
var errTokenBlacklisted = errTokenBlacklistedType{}

// errTokenBlacklistedType 是 token 被拉黑的错误类型。
type errTokenBlacklistedType struct{}

func (errTokenBlacklistedType) Error() string { return "token has been revoked" }

// NewBlacklistCheckerFromRedis 用 Redis 构造黑名单查询器。
//
// rdb 为 nil 时返 nil —— 查不了黑名单，降级放行。
func NewBlacklistCheckerFromRedis(rdb *redis.Client) middleware.TokenChecker {
	if rdb == nil {
		return nil
	}
	return middleware.NewBlacklistChecker(rdb)
}
