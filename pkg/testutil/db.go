package testutil

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// RootDSNEnv 是集成测试用的 root DSN 环境变量名。没设置时相关测试 skip。
const RootDSNEnv = "CHATROOM_TEST_ROOT_DSN"

// DB 测试用的临时数据库句柄。
type DB struct {
	Source  string // 带库名的 DSN —— 各域的 ent client 用它连库
	Cleanup func() // 删库。测试结束时调用
}

// NewDB 建一个临时库，跑完自动 DROP。每个用例一个独立库。
// root DSN 形如 `root:pass@tcp(host:port)/?args`（不带库名），没设置就 skip。
func NewDB(t *testing.T) *DB {
	t.Helper()

	root := os.Getenv(RootDSNEnv)
	if root == "" {
		t.Skipf("%s not set; skipping data-layer test", RootDSNEnv)
	}

	rootDB, err := sql.Open("mysql", root)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer rootDB.Close()

	dbName := fmt.Sprintf("chatroom_test_%d", time.Now().UnixNano())
	if _, err := rootDB.Exec("CREATE DATABASE `" + dbName + "` DEFAULT CHARACTER SET utf8mb4"); err != nil {
		t.Fatalf("create db: %v", err)
	}

	cleanup := func() {
		db, err := sql.Open("mysql", root)
		if err != nil {
			return
		}
		defer db.Close()
		_, _ = db.Exec("DROP DATABASE IF EXISTS `" + dbName + "`")
	}

	db := &DB{
		Source:  InjectDBName(root, dbName),
		Cleanup: cleanup,
	}
	t.Cleanup(cleanup)
	return db
}

// InjectDBName 把 root DSN（无库名或库名为空）拼上具体库名
func InjectDBName(rootDSN, name string) string {
	for i := 0; i < len(rootDSN); i++ {
		if rootDSN[i] == '/' {
			rest := rootDSN[i+1:]
			if rest == "" || rest[0] == '?' {
				return rootDSN[:i+1] + name + rest
			}
			// 跳过现有库名，直到 '?' 或结尾
			j := 0
			for j < len(rest) && rest[j] != '?' {
				j++
			}
			return rootDSN[:i+1] + name + rest[j:]
		}
	}
	return rootDSN
}
