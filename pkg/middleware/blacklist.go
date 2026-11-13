package middleware

import (
	"context"

	"chatroom/pkg/keys"

	"github.com/redis/go-redis/v9"
)

// BlacklistChecker 是基于 Redis 的 access token 黑名单查询器。
// Redis 不可达时放行。
type BlacklistChecker struct {
	rdb redis.Cmdable
}

// NewBlacklistChecker 构造黑名单查询器。
// rdb 为 nil 时恒放行
func NewBlacklistChecker(rdb redis.Cmdable) *BlacklistChecker {
	return &BlacklistChecker{rdb: rdb}
}

// blacklistKey 黑名单 key，与写入方共用。
// jti token 的唯一标识
func blacklistKey(jti string) string {
	return keys.TokenBlacklistKey(jti)
}

// IsBlacklisted 查询 jti 是否在黑名单里。
func (c *BlacklistChecker) IsBlacklisted(ctx context.Context, jti string) (bool, error) {
	if c == nil || c.rdb == nil {
		return false, nil
	}
	n, err := c.rdb.Exists(ctx, blacklistKey(jti)).Result()
	if err != nil {
		return false, nil
	}
	return n > 0, nil
}

// 编译期检查 BlacklistChecker 满足 TokenChecker。
var _ TokenChecker = (*BlacklistChecker)(nil)
