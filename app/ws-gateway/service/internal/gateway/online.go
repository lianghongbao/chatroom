package gateway

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// OnlineReporter 在线状态上报（Redis）。`online:{uid}` 的值 = 网关实例 ID，
// TTL 随心跳续期。存 Redis 是为了多实例下也能看到全局在线状态。
type OnlineReporter struct {
	rdb        redis.Cmdable
	instanceID string
	ttl        time.Duration
}

// NewOnlineReporter 构造 OnlineReporter。rdb 为 nil 时降级（不写在线状态）。
func NewOnlineReporter(rdb redis.Cmdable, instanceID string, ttl time.Duration) *OnlineReporter {
	if ttl <= 0 {
		ttl = defaultOnlineTTL
	}
	return &OnlineReporter{rdb: rdb, instanceID: instanceID, ttl: ttl}
}

// MarkOnline 标记用户在线。
func (r *OnlineReporter) MarkOnline(ctx context.Context, userID string) {
	if r == nil || r.rdb == nil {
		return
	}
	key := onlineKey(userID)
	// SET 带 TTL —— 心跳时重复调用等于续期
	if err := r.rdb.Set(ctx, key, r.instanceID, r.ttl).Err(); err != nil {
		return
	}
}

// MarkOffline 标记用户离线。只有当 Redis 里记的还是本实例时才删 ——
// 用户可能同时连着另一个实例。
func (r *OnlineReporter) MarkOffline(ctx context.Context, userID string) {
	if r == nil || r.rdb == nil {
		return
	}
	key := onlineKey(userID)
	// Lua 做「比较并删」的原子操作。分两步（GET 再 DEL）有竞态：
	// 用户在另一个实例上线时会把新状态误删
	const script = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0
`
	if err := r.rdb.Eval(ctx, script, []string{key}, r.instanceID).Err(); err != nil {
		return
	}
}

// IsOnline 查用户是否在线。
func (r *OnlineReporter) IsOnline(ctx context.Context, userID string) bool {
	if r == nil || r.rdb == nil {
		return false
	}
	n, err := r.rdb.Exists(ctx, onlineKey(userID)).Result()
	if err != nil {
		return false
	}
	return n > 0
}

// onlineKey 在线状态的 Redis key。
func onlineKey(userID string) string {
	return "online:" + userID
}
