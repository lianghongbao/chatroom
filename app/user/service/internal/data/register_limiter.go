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
//
// 两个桶：注册桶和用户名校验桶。分开的原因见 biz.RegisterLimiter 的说明 ——
// 注册页会连发好几次校验，共用一个桶的话用户还没提交注册就被限流了。
type registerLimiter struct {
	rdb redis.Cmdable
	// bucket 是注册桶。提交注册时扣。
	bucket *limiter.Bucket
	// checkBucket 是用户名校验桶。校验用户名时扣。
	checkBucket *limiter.Bucket
}

// defaultRegisterCapacity 注册桶的默认容量。
const defaultRegisterCapacity = 10

// defaultRegisterRate 注册桶的默认速率（每秒补几个令牌）。
const defaultRegisterRate = 5.0 / 60.0

// defaultRegisterTTL 注册桶的空闲回收时间。
const defaultRegisterTTL = 5 * time.Minute

// defaultCheckCapacity 用户名校验桶的默认容量。
//
// 比注册桶大：一次注册流程可能连发十几次校验（每个停顿 + 每次失焦），
// 容量太小会误伤正常用户。
const defaultCheckCapacity = 60

// defaultCheckRate 用户名校验桶的默认速率（每秒补几个令牌）。
//
// 比注册桶宽松 —— 校验是交互式的，用户停顿时就该有结果。
const defaultCheckRate = 2.0

// defaultCheckTTL 用户名校验桶的空闲回收时间。
const defaultCheckTTL = 5 * time.Minute

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
	// 校验桶有独立的配置段；没配就用代码里的默认值。
	checkCfg := limits.GetCheckUsername()
	checkCapacity := defaultCheckCapacity
	checkRate := defaultCheckRate
	checkTTL := defaultCheckTTL
	if checkCfg != nil {
		if checkCfg.GetCapacity() > 0 {
			checkCapacity = int(checkCfg.GetCapacity())
		}
		if checkCfg.GetRate() > 0 {
			checkRate = checkCfg.GetRate()
		}
		if t := checkCfg.GetTtl(); t != nil && t.AsDuration() > 0 {
			checkTTL = t.AsDuration()
		}
	}
	return &registerLimiter{
		rdb: d.Redis(),
		bucket: limiter.New(d.Redis(),
			limiter.WithCapacity(float64(capacity)),
			limiter.WithRate(rate),
			limiter.WithTTL(ttl),
		),
		checkBucket: limiter.New(d.Redis(),
			limiter.WithCapacity(float64(checkCapacity)),
			limiter.WithRate(checkRate),
			limiter.WithTTL(checkTTL),
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

// AllowCheck 检查这个 IP 还能不能查用户名占用；超配额返 ErrUserRateLimited。
//
// 用**独立**的桶（scope "check_username"），不跟注册桶混用 ——
// 注册页的防抖校验会连发好几次，混用会把自己的注册名额耗光。
//
// clientIP 调用方 IP；Redis 错降级放行
func (l *registerLimiter) AllowCheck(ctx context.Context, clientIP string) error {
	if l == nil || l.rdb == nil {
		return nil
	}
	key := keys.LimitBucketKey("check_username", clientIP)
	if res := l.checkBucket.Allow(ctx, key, time.Time{}); !res.Allowed {
		return biz.ErrUserRateLimited
	}
	return nil
}
