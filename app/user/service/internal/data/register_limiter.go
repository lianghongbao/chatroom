package data

import (
	"context"
	"time"

	"chatroom/app/user/service/internal/biz"
	"chatroom/app/user/service/internal/conf"
	"chatroom/pkg/keys"
	"chatroom/pkg/limiter"

	"github.com/redis/go-redis/v9"
)

// registerLimiter 注册限频器 —— Redis + Lua 令牌桶。
//
// 维度：**只按 IP**。
type registerLimiter struct {
	rdb    redis.Cmdable
	bucket *limiter.Bucket
}

// defaultRegisterCapacity 注册桶的默认容量。
const defaultRegisterCapacity = 10

// defaultRegisterRate 注册桶的默认速率（每秒补几个令牌）。
const defaultRegisterRate = 5.0 / 60.0

// defaultRegisterTTL 注册桶的空闲回收时间。
const defaultRegisterTTL = 5 * time.Minute

// NewRegisterLimiter 构造注册限频器。
//
// d 存储客户端
// limits 限流配置段
// Redis / limits 任一缺失返 nil，biz 层关掉限频。
func NewRegisterLimiter(d *Data, limits *conf.RateLimits) biz.RegisterLimiter {
	if d == nil || d.Redis() == nil {
		return nil
	}
	cfg := limits.GetRegister()
	capacity := defaultRegisterCapacity
	rate := defaultRegisterRate
	ttl := defaultRegisterTTL
	if cfg != nil {
		if cfg.GetCapacity() > 0 {
			capacity = int(cfg.GetCapacity())
		}
		if cfg.GetRate() > 0 {
			rate = cfg.GetRate()
		}
		if t := cfg.GetTtl(); t != nil && t.AsDuration() > 0 {
			ttl = t.AsDuration()
		}
	}
	return &registerLimiter{
		rdb: d.Redis(),
		bucket: limiter.New(d.Redis(),
			limiter.WithCapacity(float64(capacity)),
			limiter.WithRate(rate),
			limiter.WithTTL(ttl),
		),
	}
}

// Allow 检查这个 IP 还能不能注册；超配额返 ErrUserRateLimited。
//
// clientIP 调用方 IP；Redis 错降级放行
func (l *registerLimiter) Allow(ctx context.Context, clientIP string) error {
	if l == nil || l.rdb == nil {
		return nil
	}
	key := keys.LimitBucketKey("register", clientIP)
	if res := l.bucket.Allow(ctx, key, time.Time{}); !res.Allowed {
		return biz.ErrUserRateLimited
	}
	return nil
}
