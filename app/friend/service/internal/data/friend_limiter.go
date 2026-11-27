package data

import (
	"context"
	"time"

	"chatroom/app/friend/service/internal/biz"
	"chatroom/pkg/keys"
	"chatroom/pkg/limiter"

	"github.com/go-kratos/kratos/v3/log"
	"github.com/redis/go-redis/v9"
)

// FriendLimiter 是 biz.FriendLimiter 的 Redis + Lua 令牌桶实现。
//
// 每个动作一个桶，维度是「操作 + 调用方 uid」。
// 桶的存量是「还允许做几次」—— 消耗式令牌桶，
// 稳态速率由 rate 控制，突发量由 capacity 控制。
//
// 为什么不是固定窗口：固定窗口在边界会放行两倍流量
// （第 59 秒 30 次 + 第 61 秒 30 次 = 一分钟 60 次配额）。
// 令牌桶匀速发牌，稳态速率卡死，边界卡不出额外配额。
//
// Redis 不可达一律放行并打 warn 日志 —— 限流是保护措施，
// Redis 抖一下把全部好友请求拒掉比不限流更糟。
type FriendLimiter struct {
	rdb redis.Cmdable
	// 每个动作一个桶：
	//   apply：高频但低价值，允许稍宽
	//   acceptReject：处理收到的请求，正常频率较低
	//   remove：低频高价值操作，最严格
	apply        *limiter.Bucket
	acceptReject *limiter.Bucket
	remove       *limiter.Bucket
}

// defaultFriendLimiter 限频默认值。可在 conf 缺失或为 0 时回退到下面这组值。
//
// 三组参数的共同形状：capacity 是突发量，rate 是稳态速率（次/秒），
// ttl 是桶的空闲回收时间。
var defaultFriendLimiter = struct {
	applyCapacity        float64
	applyRate            float64
	applyTTL             time.Duration
	acceptRejectCapacity float64
	acceptRejectRate     float64
	acceptRejectTTL      time.Duration
	removeCapacity       float64
	removeRate           float64
	removeTTL            time.Duration
}{
	// 申请好友：稳态 30 次/分钟 = 0.5 次/秒，突发 30。
	applyCapacity:        30,
	applyRate:            30.0 / 60.0,
	applyTTL:             time.Minute,
	// 处理收到的申请：稳态 60 次/分钟 = 1 次/秒，突发 60。
	acceptRejectCapacity: 60,
	acceptRejectRate:     60.0 / 60.0,
	acceptRejectTTL:      time.Minute,
	// 删除好友：稳态 30 次/分钟 = 0.5 次/秒，突发 30。
	removeCapacity:       30,
	removeRate:           30.0 / 60.0,
	removeTTL:            time.Minute,
}

// NewFriendLimiter 构造好友域频控。d 是数据层实例；Redis 不可用时返回 nil，由 biz 层走 noop。
func NewFriendLimiter(d *Data) biz.FriendLimiter {
	if d == nil || d.Redis() == nil {
		return nil
	}
	return &FriendLimiter{
		rdb: d.Redis(),
		apply: limiter.New(d.Redis(),
			limiter.WithCapacity(defaultFriendLimiter.applyCapacity),
			limiter.WithRate(defaultFriendLimiter.applyRate),
			limiter.WithTTL(defaultFriendLimiter.applyTTL),
		),
		acceptReject: limiter.New(d.Redis(),
			limiter.WithCapacity(defaultFriendLimiter.acceptRejectCapacity),
			limiter.WithRate(defaultFriendLimiter.acceptRejectRate),
			limiter.WithTTL(defaultFriendLimiter.acceptRejectTTL),
		),
		remove: limiter.New(d.Redis(),
			limiter.WithCapacity(defaultFriendLimiter.removeCapacity),
			limiter.WithRate(defaultFriendLimiter.removeRate),
			limiter.WithTTL(defaultFriendLimiter.removeTTL),
		),
	}
}

// AllowApply 检查是否还能发好友申请。callerID 是操作者 ID。
func (l *FriendLimiter) AllowApply(ctx context.Context, callerID string) error {
	if l == nil {
		return nil
	}
	if !l.allow(ctx, l.apply, l.applyKey(callerID)) {
		return biz.ErrFriendRateLimited
	}
	return nil
}

// AllowAcceptReject 检查是否还能处理收到的申请。callerID 是操作者 ID。
func (l *FriendLimiter) AllowAcceptReject(ctx context.Context, callerID string) error {
	if l == nil {
		return nil
	}
	if !l.allow(ctx, l.acceptReject, l.acceptRejectKey(callerID)) {
		return biz.ErrFriendRateLimited
	}
	return nil
}

// AllowRemove 检查是否还能删好友。callerID 是操作者 ID。
func (l *FriendLimiter) AllowRemove(ctx context.Context, callerID string) error {
	if l == nil {
		return nil
	}
	if !l.allow(ctx, l.remove, l.removeKey(callerID)) {
		return biz.ErrFriendRateLimited
	}
	return nil
}

// allow 从桶里取一个令牌。
// bucket 令牌桶
// key 桶的 redis key
// 返回 true 表示放行；Redis err 一律降级放行。
func (l *FriendLimiter) allow(ctx context.Context, bucket *limiter.Bucket, key string) bool {
	if l == nil || l.rdb == nil {
		return true
	}
	res := bucket.Allow(ctx, key, time.Time{})
	if !res.Allowed {
		log.Warn("friend limiter 限流命中", "key", key)
	}
	return res.Allowed
}

// applyKey 返回 chatroom:limit:friend:apply:<callerID>。callerID 为空时用占位符。
func (l *FriendLimiter) applyKey(callerID string) string {
	return keys.LimitBucketKey("friend:apply", callerID)
}

// acceptRejectKey 返回 chatroom:limit:friend:accept_reject:<callerID>。
func (l *FriendLimiter) acceptRejectKey(callerID string) string {
	return keys.LimitBucketKey("friend:accept_reject", callerID)
}

// removeKey 返回 chatroom:limit:friend:remove:<callerID>。
func (l *FriendLimiter) removeKey(callerID string) string {
	return keys.LimitBucketKey("friend:remove", callerID)
}
