package dbpool_test

import (
	"database/sql"
	"testing"
	"time"

	"chatroom/pkg/dbpool"

	_ "github.com/go-sql-driver/mysql"
)

// 漏填必须收紧到默认值，不能退回 database/sql 的危险默认。
func TestResolveFillsDefaults(t *testing.T) {
	got := dbpool.Resolve(dbpool.Options{})
	if got.MaxOpenConns != dbpool.DefaultMaxOpenConns {
		t.Fatalf("MaxOpenConns 应为 %d, got %d", dbpool.DefaultMaxOpenConns, got.MaxOpenConns)
	}
	if got.MaxIdleConns != dbpool.DefaultMaxIdleConns {
		t.Fatalf("MaxIdleConns 应为 %d, got %d", dbpool.DefaultMaxIdleConns, got.MaxIdleConns)
	}
	if got.ConnMaxLifetime != dbpool.DefaultConnMaxLifetime {
		t.Fatalf("ConnMaxLifetime 应为 %v, got %v", dbpool.DefaultConnMaxLifetime, got.ConnMaxLifetime)
	}
	if got.ConnMaxIdleTime != dbpool.DefaultConnMaxIdleTime {
		t.Fatalf("ConnMaxIdleTime 应为 %v, got %v", dbpool.DefaultConnMaxIdleTime, got.ConnMaxIdleTime)
	}
}

// 负数也当没配 —— 不能因为写错一个负号就把限制放开。
func TestResolveTreatsNegativeAsUnset(t *testing.T) {
	got := dbpool.Resolve(dbpool.Options{
		MaxOpenConns:    -1,
		MaxIdleConns:    -5,
		ConnMaxLifetime: -time.Second,
		ConnMaxIdleTime: -time.Second,
	})
	if got.MaxOpenConns != dbpool.DefaultMaxOpenConns {
		t.Fatalf("负数应回落默认值, got %d", got.MaxOpenConns)
	}
	if got.ConnMaxLifetime != dbpool.DefaultConnMaxLifetime {
		t.Fatalf("负数应回落默认值, got %v", got.ConnMaxLifetime)
	}
}

func TestResolveKeepsExplicitValues(t *testing.T) {
	got := dbpool.Resolve(dbpool.Options{
		MaxOpenConns:    3,
		MaxIdleConns:    2,
		ConnMaxLifetime: time.Minute,
		ConnMaxIdleTime: 10 * time.Second,
	})
	if got.MaxOpenConns != 3 || got.MaxIdleConns != 2 {
		t.Fatalf("显式值不应被覆盖: %+v", got)
	}
	if got.ConnMaxLifetime != time.Minute || got.ConnMaxIdleTime != 10*time.Second {
		t.Fatalf("显式值不应被覆盖: %+v", got)
	}
}

// Apply 要真的落到 *sql.DB 上。
func TestApplySetsMaxOpenConnections(t *testing.T) {
	db, err := sql.Open("mysql", "root:pw@tcp(127.0.0.1:1)/test")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	dbpool.Apply(db, dbpool.Options{MaxOpenConns: 7})
	if got := db.Stats().MaxOpenConnections; got != 7 {
		t.Fatalf("MaxOpenConnections 应为 7, got %d", got)
	}
}

func TestApplyNilDBDoesNotPanic(t *testing.T) {
	dbpool.Apply(nil, dbpool.Options{MaxOpenConns: 7})
}
