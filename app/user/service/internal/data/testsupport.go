package data

import (
	"context"
	"database/sql"

	"chatroom/app/user/service/internal/data/ent"

	"entgo.io/ent/dialect/sql/schema"
)

// NewDataForTest 构造一个只连数据库、不连 Redis 的 *Data。
// client 是 ent 客户端，sqlDB 是它背后的同一个连接池。
// 调用方必须自己保证目标库已经建好本域的表。Redis 一律为 nil。
//
// sqlDB 必须单独传入 —— 没有它的话依赖原生 SQL 的
// 路径在测试里会拿不到连接。
func NewDataForTest(client *ent.Client, sqlDB *sql.DB) *Data {
	return &Data{db: client, sqlDB: sqlDB}
}

// Migrate 建本域的表。ctx 是上下文，client 是 ent 客户端。
// 集成测试用它把 schema 迁到临时库。
func Migrate(ctx context.Context, client *ent.Client) error {
	return client.Schema.Create(ctx, schema.WithForeignKeys(false))
}
