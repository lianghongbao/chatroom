// Package dbpool 定义数据库连接池的参数与默认值。
//
// 抽在 pkg 里是因为这几个值所有服务必须一致 —— 连接池跟 MySQL 实例
// 共享，各服务各配各的很容易配出「总量刚好超过 max_connections」。
//
// 不能留 database/sql 的默认值：MaxOpenConns 默认无限、
// MaxIdleConns 默认 2、ConnMaxLifetime 默认永不复用。
package dbpool

import (
	"database/sql"
	"time"
)

// 连接池默认值。按「MySQL 默认 max_connections=151，5 个服务共库，
// 每服务最多 10 副本」倒推：10 副本 × 10 连接 = 100 连接/服务。
// 连接数是所有服务共享的，不是每个服务各有一份。
const (
	// DefaultMaxOpenConns 单个进程最多持有多少条连接。
	DefaultMaxOpenConns = 10
	// DefaultMaxIdleConns 空闲连接池大小。略小于 MaxOpenConns，
	// 留余量给突发，但囤着的空闲连接同样占 MySQL 的连接额度。
	DefaultMaxIdleConns = 5
	// DefaultConnMaxLifetime 连接的最长存活时间。到期还掉重建，
	// 保证连接不比 MySQL 侧的空闲超时更长寿。
	DefaultConnMaxLifetime = 30 * time.Minute
	// DefaultConnMaxIdleTime 空闲连接的最长保留时间。
	DefaultConnMaxIdleTime = 5 * time.Minute
)

// Options 是连接池参数。每一项 <= 0 时用对应的默认值。
type Options struct {
	// MaxOpenConns 单进程连接数上限。
	MaxOpenConns int
	// MaxIdleConns 空闲连接数。
	MaxIdleConns int
	// ConnMaxLifetime 连接最长存活时间。
	ConnMaxLifetime time.Duration
	// ConnMaxIdleTime 空闲连接最长保留时间。
	ConnMaxIdleTime time.Duration
}

// Resolve 补齐缺省值。每一项 <= 0 都用默认值 —— 配置漏填不等于
// 「不要限制」。
func Resolve(o Options) Options {
	if o.MaxOpenConns <= 0 {
		o.MaxOpenConns = DefaultMaxOpenConns
	}
	if o.MaxIdleConns <= 0 {
		o.MaxIdleConns = DefaultMaxIdleConns
	}
	if o.ConnMaxLifetime <= 0 {
		o.ConnMaxLifetime = DefaultConnMaxLifetime
	}
	if o.ConnMaxIdleTime <= 0 {
		o.ConnMaxIdleTime = DefaultConnMaxIdleTime
	}
	return o
}

// Apply 把参数落到 *sql.DB 上。db 必须已经建好（sql.Open 即可），
// 为 nil 时什么都不做。
func Apply(db *sql.DB, o Options) {
	if db == nil {
		return
	}
	o = Resolve(o)
	db.SetMaxOpenConns(o.MaxOpenConns)
	db.SetMaxIdleConns(o.MaxIdleConns)
	db.SetConnMaxLifetime(o.ConnMaxLifetime)
	db.SetConnMaxIdleTime(o.ConnMaxIdleTime)
}
