package data

import (
	"context"
	"errors"

	"chatroom/pkg/health"
)

// PingMySQL 探 MySQL。ctx 是探针超时上下文。
//
// 用 sqlDB 而不是 ent —— ent 的 PingContext 语义一样，但直接用 sql.DB
// 少一层类型断言，且能明确探的是「连接池能不能拿到连接」。
func (d *Data) PingMySQL(ctx context.Context) error {
	if d == nil || d.sqlDB == nil {
		return errors.New("mysql client not configured")
	}
	return d.sqlDB.PingContext(ctx)
}

// PingRedis 探 Redis。ctx 是探针超时上下文。
func (d *Data) PingRedis(ctx context.Context) error {
	if d == nil || d.rdb == nil {
		return errors.New("redis client not configured")
	}
	return d.rdb.Ping(ctx).Err()
}

// NewHealthChecker 构造本服务的就绪检查器。
//
// 依赖分类只看一个问题：这个依赖挂了，服务还能不能正确处理请求？
//
//   - MySQL 是硬依赖：没有它什么都读不了写不了。
//   - Redis 是软依赖：限频、黑名单、缓存、广播都是降级放行。
//     标成硬依赖会让 Redis 一抖就把所有服务的 pod 全摘光，故障面比降级本身大得多。
func NewHealthChecker(d *Data) *health.Checker {
	c := health.NewChecker(health.DefaultTimeout)
	c.Add("mysql", true, d.PingMySQL)
	c.Add("redis", false, d.PingRedis)
	return c
}
