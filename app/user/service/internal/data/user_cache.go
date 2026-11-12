package data

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"chatroom/user-service/internal/biz"

	"github.com/go-kratos/kratos/v3/log"
	"github.com/redis/go-redis/v9"
)

// UserCache biz.UserCache 的 Redis 实现。两个 key：user:{uid} 存 JSON（不含 password_hash），user:ver:{uid} 存版本号。
// cacheEntry 故意不带 PasswordHash 字段 —— 序列化层最后一道保险，hash 永不入缓存。
type UserCache struct {
	rdb redis.Cmdable
	ttl time.Duration
}

// NewUserCache 构造 UserCache。Data / Redis 不可用时返 nil（biz 走降级直接读 DB）。
// TTL 30s 是 UpdateUser 漏走 invalidate 时的兜底。
func NewUserCache(d *Data) *UserCache {
	if d == nil || d.Redis() == nil {
		return nil
	}
	return &UserCache{rdb: d.Redis(), ttl: 30 * time.Second}
}

func userKey(uid string) string {
	return "chatroom:user:" + uid
}

func verKey(uid string) string {
	return "chatroom:user:ver:" + uid
}

// cacheEntry 是缓存里存的用户资料。
//
// 字段必须跟 biz.User 对齐 —— 少存一个字段，读出来就少一个值：
//   - Status 少了会变成 0 = UserStatusBanned（正常账号被当成已禁用）
//   - IsVisibleToStranger 少了会变成 false（陌生人可见性判断反了）
//   - Email / Phone 少了会变成空串（本人看自己的资料时是空的）
type cacheEntry struct {
	ID                  string    `json:"id"`
	Username            string    `json:"username"`
	Nickname            string    `json:"nickname"`
	Avatar              string    `json:"avatar"`
	Email               string    `json:"email"`
	Phone               string    `json:"phone"`
	IsVisibleToStranger bool      `json:"is_visible_to_stranger"`
	Status              uint8     `json:"status"`
	CreatedAt           time.Time `json:"created_at"`
	Ver                 int64     `json:"ver"`
}

// Get 命中 (user, ver, nil)；未命中 (nil, 0, nil)；Redis 错 (nil, 0, err)。
func (c *UserCache) Get(ctx context.Context, id string) (*biz.User, int64, error) {
	if c == nil || c.rdb == nil {
		return nil, 0, nil
	}
	raw, err := c.rdb.Get(ctx, userKey(id)).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, 0, nil
		}
		log.Warn("用户缓存读取降级", "err", err)
		return nil, 0, err
	}
	var entry cacheEntry
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		log.Warn("用户缓存反序列化失败", "err", err) // schema 演进导致，按 miss 处理
		return nil, 0, nil
	}
	return &biz.User{
		ID: entry.ID, Username: entry.Username, Nickname: entry.Nickname,
		Avatar: entry.Avatar, Email: entry.Email, Phone: entry.Phone,
		IsVisibleToStranger: entry.IsVisibleToStranger,
		Status:              biz.UserStatus(entry.Status),
		CreatedAt:           entry.CreatedAt,
	}, entry.Ver, nil
}

// Set 同时确保 verKey 存在（SETNX 0），保证 Version() 不会因 verKey 缺失让缓存值失效。
func (c *UserCache) Set(ctx context.Context, u *biz.User, ver int64) error {
	if c == nil || c.rdb == nil {
		return nil
	}
	entry := cacheEntry{
		ID: u.ID, Username: u.Username, Nickname: u.Nickname,
		Avatar: u.Avatar, Email: u.Email, Phone: u.Phone,
		IsVisibleToStranger: u.IsVisibleToStranger,
		Status:              uint8(u.Status),
		CreatedAt:           u.CreatedAt, Ver: ver,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		log.Warn("用户缓存序列化失败", "err", err)
		return nil
	}
	key := userKey(u.ID)
	pipe := c.rdb.TxPipeline()
	pipe.Set(ctx, key, data, c.ttl)
	pipe.SetNX(ctx, verKey(u.ID), strconv.FormatInt(ver, 10), 0)
	if _, err := pipe.Exec(ctx); err != nil {
		log.Warn("用户缓存写入降级", "err", err)
		return nil
	}
	return nil
}

// Invalidate 自增 verKey；verKey 不存在时 INCR 自动设为 1。
func (c *UserCache) Invalidate(ctx context.Context, id string) (int64, error) {
	if c == nil || c.rdb == nil {
		return 0, nil
	}
	n, err := c.rdb.Incr(ctx, verKey(id)).Result()
	if err != nil {
		log.Warn("用户缓存失效降级", "err", err)
		return 0, nil
	}
	return n, nil
}

// Version 读 verKey，缺失返 (0, nil)，与「第一次 GetUser 之前」对齐（此时 ver==0 命中）。
func (c *UserCache) Version(ctx context.Context, id string) (int64, error) {
	if c == nil || c.rdb == nil {
		return 0, nil
	}
	v, err := c.rdb.Get(ctx, verKey(id)).Result()
	if err != nil {
		if err == redis.Nil {
			return 0, nil
		}
		log.Warn("用户缓存版本查询降级", "err", err)
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, nil
	}
	return n, nil
}
