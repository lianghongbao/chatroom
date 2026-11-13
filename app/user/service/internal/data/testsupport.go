package data

import (
	"context"
	"database/sql"

	"chatroom/app/user/service/internal/data/ent"

	"entgo.io/ent/dialect/sql/schema"
)

// NewDataForTest 构造一个只连数据库、不连 Redis 的 *Data。
// client ent 客户端
// sqlDB 它背后的同一个连接池（依赖原生 SQL 的路径要用）
// 调用方必须自己保证目标库已经建好本域的表。Redis 一律为 nil。
func NewDataForTest(client *ent.Client, sqlDB *sql.DB) *Data {
	return &Data{db: client, sqlDB: sqlDB}
}

// Migrate 建本域的表。
// ctx 上下文
// client ent 客户端
// 集成测试用它把 schema 迁到临时库。
func Migrate(ctx context.Context, client *ent.Client) error {
	return client.Schema.Create(ctx, schema.WithForeignKeys(false))
}
