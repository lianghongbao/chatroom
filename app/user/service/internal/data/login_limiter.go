package data

import (
	"context"
	"time"

	"chatroom/app/user/service/internal/biz"
	"chatroom/app/user/service/internal/conf"
	"chatroom/pkg/keys"
	"chatroom/pkg/limiter"

	"github.com/go-kratos/kratos/v3/log"
	"github.com/redis/go-redis/v9"
)

// loginLimiter 登录限频器 —— Redis + Lua 令牌桶。
//
// 维度：用户名 + IP 两个都进 key。
type loginLimiter struct {
	rdb redis.Cmdable
	// bucket 失败计数桶。
	bucket *limiter.Bucket
	// lockKey 锁定标记的 key。桶空时置位，锁定期满自动消失。
	lockKey func(username, clientIP string) string
	// lockTTL 锁定时长。
	lockTTL time.Duration
}

// NewLoginLimiter 构造登录限频器。
//
// d 存储客户端
// auth 鉴权配置段
// limits 限流配置段
// Auth / Redis / limits 任一缺失返 nil，biz 层关掉限频。
func NewLoginLimiter(d *Data, auth *conf.Auth, limits *conf.RateLimits) biz.LoginLimiter {
	if d == nil || d.Redis() == nil || auth == nil {
		return nil
	}
	maxFail := auth.GetLoginMaxFail()
	if maxFail <= 0 {
		maxFail = 5
	}
	ttl := 15 * time.Minute
	if t := auth.GetLoginLockTtl(); t != nil && t.AsDuration() > 0 {
		ttl = t.AsDuration()
	}
	// 桶容量 = 允许的失败次数。容量就是「连续失败几次锁」。
	// 速率 = 每秒回充几个令牌 —— 决定「锁多久之后能再试」。
	rate := float64(maxFail) / ttl.Seconds()
	return &loginLimiter{
		rdb:    d.Redis(),
		bucket: newBucket(d.Redis(), limits.GetLoginFail(), float64(maxFail), rate, ttl),
		lockKey: func(u, ip string) string {
			return keys.LimitBucketKey("login:locked", u+"|"+ip)
		},
		lockTTL: ttl,
	}
}

// newBucket 按配置构造令牌桶。cfg 为 nil 时用给定的默认值。
func newBucket(rdb redis.Cmdable, cfg *conf.RateLimit, defCapacity, defRate float64, defTTL time.Duration) *limiter.Bucket {
	capacity, rate, ttl := defCapacity, defRate, defTTL
	if cfg != nil {
		if cfg.GetCapacity() > 0 {
			capacity = cfg.GetCapacity()
		}
		if cfg.GetRate() > 0 {
			rate = cfg.GetRate()
		}
		if t := cfg.GetTtl(); t != nil && t.AsDuration() > 0 {
			ttl = t.AsDuration()
		}
	}
	return limiter.New(rdb,
		limiter.WithCapacity(capacity),
		limiter.WithRate(rate),
		limiter.WithTTL(ttl),
	)
}

// Allow 检查 username 在这个 IP 上是否被锁定；命中返 ErrUserLoginLocked。
//
// username 用户名
// clientIP 调用方 IP；Redis 错降级放行
func (l *loginLimiter) Allow(ctx context.Context, username, clientIP string) error {
	if l == nil || l.rdb == nil {
		return nil
	}
	v, err := l.rdb.Get(ctx, l.lockKey(username, clientIP)).Result()
	if err != nil {
		if err != redis.Nil {
			log.Warn("登录限频查询降级", "err", err)
		}
		return nil
	}
	if v == "1" {
		return biz.ErrUserLoginLocked
	}
	return nil
}

// OnFailure 消耗一个令牌；桶空说明失败次数到了阈值，置锁定标记。
//
// username 用户名
// clientIP 调用方 IP
// 错误仅 warn，不阻断流程
func (l *loginLimiter) OnFailure(ctx context.Context, username, clientIP string) {
	if l == nil || l.rdb == nil {
		return
	}
	key := keys.LimitBucketKey("login:fail", username+"|"+clientIP)
	// 先看余额，再消耗 —— 两步都在 Lua 里原子完成。
	//
	// 为什么不用「直接 Allow（消耗）」：桶容量是 maxFail，
	// 前 maxFail 次失败各自消耗一个令牌并成功返回，
	// 第 maxFail+1 次才拿不到令牌。此时才置锁定标记 ——
	// 也就是「失败次数超过阈值才锁」。
	//
	// 但原语义是「失败达到阈值就锁」（第 5 次失败即锁），
	// 差一次。所以这里用 Peek 拿当前余额：余额为 0 说明
	// 「额度已经用完」，本次失败就是超限的那一次。
	res := l.bucket.Allow(ctx, key, time.Time{})
	if res.Allowed {
		// 这次失败消耗掉一个令牌。消耗后桶正好空了 = 达到阈值。
		// 用 Remaining == 0 判断「刚好用完」。
		if res.Remaining > 0 {
			return
		}
	}
	// 桶空了：失败次数达到阈值，置锁定标记。
	// 标记带 TTL，锁定期满自动消失，不需要额外的清理任务。
	if err := l.rdb.Set(ctx, l.lockKey(username, clientIP), "1", l.lockTTL).Err(); err != nil {
		log.Warn("登录锁定标记降级", "err", err)
	}
}

// OnSuccess 登录成功后把失败计数桶补满 —— 相当于「还回」这次不该
// 消耗的失败机会。
//
// username 用户名
// clientIP 调用方 IP
// 错误仅 warn，不阻断登录流程
func (l *loginLimiter) OnSuccess(ctx context.Context, username, clientIP string) {
	if l == nil || l.rdb == nil {
		return
	}
	failKey := keys.LimitBucketKey("login:fail", username+"|"+clientIP)
	l.bucket.Refill(ctx, failKey, time.Time{})
	// 锁定标记也一并清掉 —— 登录成功了说明之前的锁定已经过期或被手工清了。
	l.rdb.Del(ctx, l.lockKey(username, clientIP))
}
