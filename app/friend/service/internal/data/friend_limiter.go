package data

import (
	"context"
	"time"

	"chatroom/app/friend/service/internal/biz"

	"github.com/go-kratos/kratos/v3/log"
	"github.com/redis/go-redis/v9"
)

// FriendLimiter 是 biz.FriendLimiter 的 Redis 实现。
// 固定窗口计数：key = "chatroom:friend:<op>:<callerID>"，每次 INCR +1，
// 首次返回 1 时设窗口 TTL。计数 > limit 时返 ErrFriendRateLimited。
// 降级：Redis 不可达一律放行并打 warn 日志。
type FriendLimiter struct {
	rdb redis.Cmdable
	// 每个动作的 (windowTTL, limit)：
	//   apply：高频但低价值，允许稍宽
	//   acceptReject：处理收到的请求，正常频率较低
	//   remove：低频高价值操作，最严格
	applyTTL        time.Duration
	applyLimit      int32
	acceptRejectTTL time.Duration
	acceptRejectLim int32
	removeTTL       time.Duration
	removeLim       int32
}

// defaultFriendLimiter 限频默认值。可在 conf 缺失或为 0 时回退到下面这组值。
var defaultFriendLimiter = struct {
	applyTTL, acceptRejectTTL, removeTTL       time.Duration
	applyLimit, acceptRejectLimit, removeLimit int32
}{
	applyTTL:          time.Minute,
	applyLimit:        30,
	acceptRejectTTL:   time.Minute,
	acceptRejectLimit: 60,
	removeTTL:         time.Minute,
	removeLimit:       30,
}

// NewFriendLimiter 构造好友域频控。
// d 数据层实例
// 由 biz 层走 noop。返回 biz.FriendLimiter 接口，wire 无需再 Bind。
func NewFriendLimiter(d *Data) biz.FriendLimiter {
	if d == nil || d.Redis() == nil {
		return nil
	}
	return &FriendLimiter{
		rdb:             d.Redis(),
		applyTTL:        defaultFriendLimiter.applyTTL,
		applyLimit:      defaultFriendLimiter.applyLimit,
		acceptRejectTTL: defaultFriendLimiter.acceptRejectTTL,
		acceptRejectLim: defaultFriendLimiter.acceptRejectLimit,
		removeTTL:       defaultFriendLimiter.removeTTL,
		removeLim:       defaultFriendLimiter.removeLimit,
	}
}

// allow 是通用 INCR+EXPIRE 计数路径。key 是计数 key，ttl 是窗口长度，
// limit 是窗口内允许的最大次数。返回 true 表示放行。
// TTL 只在 INCR 返回 1 时设置；Redis err 一律降级放行。
func (l *FriendLimiter) allow(ctx context.Context, key string, ttl time.Duration, limit int32) bool {
	if l == nil || l.rdb == nil {
		return true
	}
	n, err := l.rdb.Incr(ctx, key).Result()
	if err != nil {
		log.Warn("friend limiter INCR 降级", "key", key, "err", err)
		return true
	}
	if n == 1 {
		// 首次进入窗口，设 TTL。
		if err := l.rdb.Expire(ctx, key, ttl).Err(); err != nil {
			log.Warn("friend limiter EXPIRE 降级", "key", key, "err", err)
		}
	}
	return n <= int64(limit)
}

// AllowApply 检查是否还能发好友申请。callerID 是操作者 ID。
func (l *FriendLimiter) AllowApply(ctx context.Context, callerID string) error {
	if !l.allow(ctx, l.applyKey(callerID), l.applyTTL, l.applyLimit) {
		return biz.ErrFriendRateLimited
	}
	return nil
}

// AllowAcceptReject 检查是否还能处理收到的申请。callerID 是操作者 ID。
func (l *FriendLimiter) AllowAcceptReject(ctx context.Context, callerID string) error {
	if !l.allow(ctx, l.acceptRejectKey(callerID), l.acceptRejectTTL, l.acceptRejectLim) {
		return biz.ErrFriendRateLimited
	}
	return nil
}

// AllowRemove 检查是否还能删好友。callerID 是操作者 ID。
func (l *FriendLimiter) AllowRemove(ctx context.Context, callerID string) error {
	if !l.allow(ctx, l.removeKey(callerID), l.removeTTL, l.removeLim) {
		return biz.ErrFriendRateLimited
	}
	return nil
}

// applyKey 返回 chatroom:friend:apply:<callerID>。callerID 为空时用占位符，
// 上游 biz 已校验过，这里再兜一次。
func (l *FriendLimiter) applyKey(callerID string) string {
	if callerID == "" {
		return "chatroom:friend:apply:_zero_"
	}
	return "chatroom:friend:apply:" + callerID
}

// acceptRejectKey 返回 chatroom:friend:accept_reject:<callerID>。
func (l *FriendLimiter) acceptRejectKey(callerID string) string {
	if callerID == "" {
		return "chatroom:friend:accept_reject:_zero_"
	}
	return "chatroom:friend:accept_reject:" + callerID
}

// removeKey 返回 chatroom:friend:remove:<callerID>。
func (l *FriendLimiter) removeKey(callerID string) string {
	if callerID == "" {
		return "chatroom:friend:remove:_zero_"
	}
	return "chatroom:friend:remove:" + callerID
}
