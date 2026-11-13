package data

import (
	"context"
	"time"

	"chatroom/app/user/service/internal/biz"
	"chatroom/app/user/service/internal/conf"

	"github.com/go-kratos/kratos/v3/log"
	"github.com/redis/go-redis/v9"
)

// loginLimiter 失败计数 + 锁定标记两 key：计数 INCR 后只在返回 1 时设 TTL，避免续期。
// 计数和锁定都按「用户名 + IP」两个维度分桶。
type loginLimiter struct {
	rdb       redis.Cmdable
	maxFail   int32
	lockTTL   time.Duration
	failKey   func(string, string) string
	lockedKey func(string, string) string
}

// NewLoginLimiter 构造登录限频器。
// d 存储客户端
// auth 鉴权配置段
// Auth / Redis 任一缺失返 nil，biz 层关掉限频。
func NewLoginLimiter(d *Data, auth *conf.Auth) biz.LoginLimiter {
	if d == nil || d.Redis() == nil || auth == nil {
		return nil
	}
	max := auth.GetLoginMaxFail()
	if max <= 0 {
		max = 5
	}
	ttl := 15 * time.Minute
	if d := auth.GetLoginLockTtl(); d != nil && d.AsDuration() > 0 {
		ttl = d.AsDuration()
	}
	return &loginLimiter{
		rdb:     d.Redis(),
		maxFail: max,
		lockTTL: ttl,
		// key 里带 IP。IP 里的 ':' 之类字符不影响 Redis key —— 它只是个字节串。
		failKey: func(u, ip string) string {
			return "chatroom:login:fail:" + u + ":" + ip
		},
		lockedKey: func(u, ip string) string {
			return "chatroom:login:locked:" + u + ":" + ip
		},
	}
}

// Allow 检查 username 在这个 IP 上是否被锁定；命中返 ErrUserLoginLocked。
// username 是用户名，clientIP 是调用方 IP。Redis 错降级放行。
func (l *loginLimiter) Allow(ctx context.Context, username, clientIP string) error {
	if l == nil || l.rdb == nil {
		return nil
	}
	v, err := l.rdb.Get(ctx, l.lockedKey(username, clientIP)).Result()
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

// OnFailure 累加失败计数；首次失败设 TTL；达到阈值置锁定标记。
// username 是用户名，clientIP 是调用方 IP。错误仅 warn，不阻断流程。
func (l *loginLimiter) OnFailure(ctx context.Context, username, clientIP string) {
	if l == nil || l.rdb == nil {
		return
	}
	key := l.failKey(username, clientIP)
	n, err := l.rdb.Incr(ctx, key).Result()
	if err != nil {
		log.Warn("登录失败计数降级", "err", err)
		return
	}
	if n == 1 {
		// 首次失败才设 TTL，避免窗口被无限续期
		if err := l.rdb.Expire(ctx, key, l.lockTTL).Err(); err != nil {
			log.Warn("设置失败计数 TTL 失败", "err", err)
		}
	}
	if n >= int64(l.maxFail) {
		if err := l.rdb.Set(ctx, l.lockedKey(username, clientIP), "1", l.lockTTL).Err(); err != nil {
			log.Warn("置锁定标记降级", "err", err)
		}
	}
}

// OnSuccess 清掉失败计数与锁定标记；DEL 返 0 也算成功。
// username 是用户名，clientIP 是调用方 IP。错误仅 warn，不阻断登录流程。
func (l *loginLimiter) OnSuccess(ctx context.Context, username, clientIP string) {
	if l == nil || l.rdb == nil {
		return
	}
	if err := l.rdb.Del(ctx, l.failKey(username, clientIP), l.lockedKey(username, clientIP)).Err(); err != nil {
		log.Warn("登录成功清计数降级", "err", err)
	}
}
