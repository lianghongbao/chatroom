package data

import (
	"context"
	"database/sql"

	"chatroom/app/user/service/internal/data/ent"

	"entgo.io/ent/dialect/sql/schema"
)

// NewDataForTest 构造一个只连数据库、不连 Redis 的 *Data。
// client 是 ent 客户端，
// sqlDB 是它背后的同一个连接池（依赖原生 SQL 的路径要用）；调用方必须自己保证目标库已建好本域的表。
func NewDataForTest(client *ent.Client, sqlDB *sql.DB) *Data {
	return &Data{db: client, sqlDB: sqlDB}
}

// Migrate 建本域的表。
// ctx 上下文
// client ent 客户端
func Migrate(ctx context.Context, client *ent.Client) error {
	return client.Schema.Create(ctx, schema.WithForeignKeys(false))
}
