package data

import (
	"context"
	"database/sql"

	"chatroom/app/conversation/service/internal/data/ent"

	"entgo.io/ent/dialect/sql/schema"
)

// NewDataForTest 构造一个只连数据库、不连 Redis 的 *Data。
// 调用方必须自己保证目标库已经建好本域的表。Redis 一律为 nil。
// sqlDB 由调用方单独传入（跟 ent 共用同一个池）—— 没有它的话依赖原生 SQL 的
// 路径（如未读数的原子增减）在测试里会拿不到连接。
func NewDataForTest(client *ent.Client, sqlDB *sql.DB) *Data {
	return &Data{db: client, sqlDB: sqlDB}
}

// Migrate 建本域的表。集成测试用它把各域的 schema 迁到同一个临时库。
func Migrate(ctx context.Context, client *ent.Client) error {
	return client.Schema.Create(ctx, schema.WithForeignKeys(false))
}
