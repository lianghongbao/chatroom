package data

import (
	"context"
	"database/sql"
	"testing"

	"chatroom/app/friend/service/internal/data/ent"
	"chatroom/pkg/testutil"

	entsql "entgo.io/ent/dialect/sql"
	entschema "entgo.io/ent/dialect/sql/schema"
)

// newTestDB 为每个测试创建一个独立的临时库，跑 schema migrate，
// 测试结束后自动 DROP。互不污染，也避免和 config.yaml 里共享的
// `test` 库冲突。需要 CHATROOM_TEST_ROOT_DSN 环境变量（root DSN，不带库名）；
// 没设置时 skip —— `go test ./...` 应该在没有 MySQL 的机器上也能跑。
// 返回数据层实例与清理函数。
func newTestDB(t *testing.T) (*Data, func()) {
	t.Helper()

	db := testutil.NewDB(t)

	// 自己开 *sql.DB 再交给 ent —— 测试里也要跟生产一致地共用一个池，
	// 依赖原生 SQL 的路径才能拿到连接。
	sqlDB, err := sql.Open("mysql", db.Source)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB("mysql", sqlDB)))
	if err := client.Schema.Create(context.Background(), entschema.WithForeignKeys(false)); err != nil {
		client.Close()
		t.Fatalf("schema create: %v", err)
	}
	t.Cleanup(func() {
		client.Close()
	})

	return &Data{db: client, sqlDB: sqlDB}, func() {}
}
