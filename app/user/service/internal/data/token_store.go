package data

import (
	"context"
	"time"

	"chatroom/pkg/keys"

	"github.com/redis/go-redis/v9"
)

// TokenStore biz.TokenStore 的 Redis 实现。
// 同时实现 middleware.TokenChecker —— 中间件查黑名单问的是同一份数据。
type TokenStore struct {
	rdb redis.Cmdable
}

// NewTokenStore 构造 TokenStore。d 是存储客户端；
// Redis 不可用时返 nil（黑名单功能降级）。
func NewTokenStore(d *Data) *TokenStore {
	if d == nil || d.Redis() == nil {
		return nil
	}
	return &TokenStore{rdb: d.Redis()}
}

// Blacklist 把 access token 的 JTI 加入黑名单。jti 是 token 的 JTI claim，
// ttl 是 token 的剩余寿命。
func (s *TokenStore) Blacklist(ctx context.Context, jti string, ttl time.Duration) error {
	if s == nil || s.rdb == nil {
		return nil
	}
	if jti == "" {
		return nil
	}
	return s.rdb.Set(ctx, keys.TokenBlacklistKey(jti), "1", ttl).Err()
}

// IsBlacklisted 查询 JTI 是否在黑名单里。jti 是 token 的 JTI claim。
func (s *TokenStore) IsBlacklisted(ctx context.Context, jti string) (bool, error) {
	if s == nil || s.rdb == nil {
		return false, nil
	}
	if jti == "" {
		return false, nil
	}
	n, err := s.rdb.Exists(ctx, keys.TokenBlacklistKey(jti)).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// SaveRefresh 存 refresh token 的 JTI → uid 映射。jti 是 token 的 JTI claim，
// uid 是用户 ID，userAgent 是客户端标识，ttl 是 refresh token 寿命。
func (s *TokenStore) SaveRefresh(ctx context.Context, jti string, uid string, userAgent string, ttl time.Duration) error {
	if s == nil || s.rdb == nil {
		return nil
	}
	if jti == "" {
		return nil
	}
	return s.rdb.Set(ctx, keys.RefreshTokenKey(jti), uid, ttl).Err()
}

// ConsumeRefresh 原子取出并删除 JTI → uid 映射（GETDEL）。jti 是 token 的 JTI claim。
// 一次性消费：同一个 refresh token 不能用两次。
func (s *TokenStore) ConsumeRefresh(ctx context.Context, jti string) (string, error) {
	if s == nil || s.rdb == nil {
		return "", nil
	}
	if jti == "" {
		return "", nil
	}
	return s.rdb.GetDel(ctx, keys.RefreshTokenKey(jti)).Result()
}

// RevokeRefresh 删除 JTI → uid 映射（登出 / 轮换旧 token）。jti 是 token 的 JTI claim。
func (s *TokenStore) RevokeRefresh(ctx context.Context, jti string) error {
	if s == nil || s.rdb == nil {
		return nil
	}
	if jti == "" {
		return nil
	}
	return s.rdb.Del(ctx, keys.RefreshTokenKey(jti)).Err()
}
