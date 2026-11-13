package data

import (
	"context"
	"time"

	"chatroom/app/friend/service/internal/biz"

	"github.com/go-kratos/kratos/v3/log"
	"github.com/redis/go-redis/v9"
)

// FriendLimiter 是 biz.FriendLimiter 的 Redis 实现。
//
// 好友域写路径（Apply / AcceptReject / Remove）有被刷成 DoS 的风险 ——
// 单用户可以朝自己狂发好友请求 / 狂点接受 / 狂删好友，把 DB 写穿。
// 频控要「在线可观测、低延迟、跨实例共享」，所以选 Redis 而不是进程内 limiter。
//
// 算法：固定窗口（fixed-window）计数。
//   - key = "chatroom:friend:<op>:<callerID>"
//   - 每次 INCR 计数 +1；首次返回 1 时设一个窗口 TTL
//   - 计数 > limit 时返 ErrFriendRateLimited
//   - 否则放行
//
// 不选 token bucket / leaky bucket：好友域不像登录失败计数那样需要
// 「失败累计锁定」语义，「每分钟最多 N 次」就够。固定窗口实现简单、
// Redis 调用 1 个 round-trip、可观测（直接 GET 看当前计数）。
//
// 降级：Redis 不可达一律放行（与 loginLimiter 行为一致），打 warn 日志。
// 频控是「防止意外 / 恶意」的安全网，不该让 Redis 抖动阻塞正常用户操作；
// DB 层另有 uk_user_friend 等约束兜底最坏情况。
type FriendLimiter struct {
	rdb redis.Cmdable
	// 每个动作的 (windowTTL, limit)。它们的语义不同，所以分开配：
	//   apply：Apply 是「高频但低价值」——发请求可能带 reject，允许稍宽
	//   acceptReject：处理收到的请求，可能带批量，正常频率较低
	//   remove：删好友是低频高价值操作，最严格
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

// NewFriendLimiter 构造好友域频控。d 是数据层实例，Data 或 Redis 任一为 nil 时返 nil，
// 由 biz 层走 noop（见 biz/friend.go 中的 noopFriendLimiter）。
//
// 返回 biz.FriendLimiter 接口——wire 可直接满足 NewFriendUsecase 的参数
// 需求，无需再 wire.Bind。和 NewLoginLimiter 同款做法。
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
// limit 是窗口内允许的最大次数。
//
// 返回 true 表示放行；false 表示命中上限。
//
// TTL 只在 INCR 返回 1 时设置——和 loginLimiter.OnFailure 同款理由：避免
// 每次都覆盖 TTL，让窗口不会被无限续期。
//
// Redis err 一律降级放行（返 true）；让 Redis 抖动不阻断主业务。
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
