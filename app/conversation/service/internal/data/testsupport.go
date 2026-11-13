package data

import (
	"context"
	"database/sql"

	"chatroom/app/conversation/service/internal/data/ent"

	"entgo.io/ent/dialect/sql/schema"
)

// NewDataForTest 构造一个只连数据库、不连 Redis 的 *Data。
// client ent 客户端
// sqlDB 它背后的同一个连接池（跟 ent 共用同一个池）
// 调用方必须自己保证目标库已经建好本域的表。Redis 一律为 nil。
func NewDataForTest(client *ent.Client, sqlDB *sql.DB) *Data {
	return &Data{db: client, sqlDB: sqlDB}
}

// Migrate 建本域的表。client 是 ent 客户端。集成测试用它把各域的 schema
// 迁到同一个临时库。
func Migrate(ctx context.Context, client *ent.Client) error {
	return client.Schema.Create(ctx, schema.WithForeignKeys(false))
}
