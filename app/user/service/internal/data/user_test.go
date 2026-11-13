package data

import (
	"context"
	"database/sql"
	"testing"

	"chatroom/app/user/service/internal/biz"
	"chatroom/app/user/service/internal/data/ent"
	"chatroom/pkg/testutil"

	entsql "entgo.io/ent/dialect/sql"
)

// newTestUserRepo 建一个接真实 MySQL 的 userRepo。t 是测试用例。
func newTestUserRepo(t *testing.T) biz.UserRepo {
	t.Helper()

	db := testutil.NewDB(t)

	// 自己开 *sql.DB 再交给 ent —— 测试里也要跟生产一致地共用一个池。
	sqlDB, err := sql.Open("mysql", db.Source)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB("mysql", sqlDB)))
	if err := Migrate(context.Background(), client); err != nil {
		client.Close()
		t.Fatalf("schema create: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	return NewUserRepo(NewDataForTest(client, sqlDB))
}

// TestUserRepo_FindByIDWithHash_KeepsHash FindByIDWithHash 必须带回密码哈希。
// FindByID 会把哈希清掉，改密拿的是它的结果就必然失败。
func TestUserRepo_FindByIDWithHash_KeepsHash(t *testing.T) {
	repo := newTestUserRepo(t)
	ctx := context.Background()

	const hash = "$2a$10$7JB720yubVSZvUI0rEqK/.VqGOZTH.ulu33dHOiBE8ByOhJIrdAu2"
	id, err := repo.Save(ctx, &biz.User{
		Username:     "alice",
		Nickname:     "Alice",
		PasswordHash: hash,
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	withHash, err := repo.FindByIDWithHash(ctx, id)
	if err != nil {
		t.Fatalf("FindByIDWithHash: %v", err)
	}
	if withHash.PasswordHash != hash {
		t.Errorf("FindByIDWithHash 拿到的哈希 = %q, want %q", withHash.PasswordHash, hash)
	}

	// 对照：FindByID 的哈希是空的，这是它本来的契约
	plain, err := repo.FindByID(ctx, id)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if plain.PasswordHash != "" {
		t.Errorf("FindByID 应该清掉哈希, got %q", plain.PasswordHash)
	}
}

// TestUserRepo_FindByIDWithHash_NotFound 查不到时返 ErrUserNotFound。
func TestUserRepo_FindByIDWithHash_NotFound(t *testing.T) {
	repo := newTestUserRepo(t)
	if _, err := repo.FindByIDWithHash(context.Background(), "no-such-id"); err == nil {
		t.Error("查不到用户时应该返错")
	}
}
