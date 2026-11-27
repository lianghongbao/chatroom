package data

import (
	"context"
	"errors"
	"time"

	"chatroom/app/user/service/internal/biz"
	"chatroom/pkg/keys"

	"github.com/redis/go-redis/v9"
)

// Locker biz.Locker 的 Redis 实现。
// 按 key 的非重入互斥 —— SET NX + TTL。Redis 不可用时退化成 no-op。
type Locker struct {
	rdb redis.Cmdable
}

// NewLocker 构造 biz.Locker 的 Redis 实现。d 是存储客户端；Redis 不可用时返 nil（不加锁）。
func NewLocker(d *Data) *Locker {
	if d == nil || d.Redis() == nil {
		return nil
	}
	return &Locker{rdb: d.Redis()}
}

// Acquire 取 key 的互斥锁。
// key 业务侧的 key
// ttl 锁的存活时间（必须覆盖临界区最坏耗时）
// 拿不到时返 (nil, errLockBusy)。
func (l *Locker) Acquire(ctx context.Context, key string, ttl time.Duration) (biz.Release, error) {
	if l == nil || l.rdb == nil {
		return func() {}, nil
	}
	lockKey := lockKeyOf(key)
	ok, err := l.rdb.SetNX(ctx, lockKey, "1", ttl).Result()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errLockBusy
	}
	return func() {
		// 只删自己持有的锁 —— DEL 是无条件删，如果锁已过期被别人抢走，
		// 无条件 DEL 会把别人的锁删掉。
		_ = l.rdb.Del(ctx, lockKey).Err()
	}, nil
}

// errLockBusy 锁被他人持有，由 service / biz 层翻译成对外错误。
var errLockBusy = errors.New("lock is held by others")

// lockKeyOf 锁的 redis key。key 是业务侧的 key。
func lockKeyOf(key string) string {
	return keys.LockKey(key)
}
