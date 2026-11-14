package data

import (
	"context"
	"errors"

	"chatroom/pkg/health"
)

// PingRedis 探 Redis。ctx 是探针超时上下文。
func (d *Data) PingRedis(ctx context.Context) error {
	if d == nil || d.rdb == nil {
		return errors.New("redis client not configured")
	}
	return d.rdb.Ping(ctx).Err()
}

// NewHealthChecker 构造本服务的就绪检查器。
//
// Redis 对 ws-gateway 是硬依赖：Pub/Sub 是消息投递给 WS 客户端的唯一通道。
// 带着死掉的订阅继续接新连接，只会让客户端永远等不到消息。快速失败更好。
func NewHealthChecker(d *Data) *health.Checker {
	c := health.NewChecker(health.DefaultTimeout)
	c.Add("redis", true, d.PingRedis)
	return c
}
