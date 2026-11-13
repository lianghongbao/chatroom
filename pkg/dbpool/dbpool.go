// Package dbpool 定义数据库连接池的参数与默认值。
// 这些值所有服务必须一致 —— 连接池跟 MySQL 实例共享。
package dbpool

import (
	"database/sql"
	"time"
)

// 连接池默认值。连接数是所有服务共享的，不是每个服务各有一份。
const (
	// DefaultMaxOpenConns 单个进程最多持有多少条连接。
	DefaultMaxOpenConns = 10
	// DefaultMaxIdleConns 空闲连接池大小。
	DefaultMaxIdleConns = 5
	// DefaultConnMaxLifetime 连接的最长存活时间。
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

// Resolve 补齐缺省值。每一项 <= 0 都用默认值。
// o 待补齐的参数
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

// Apply 把参数落到 *sql.DB 上。
// db 数据库句柄，为 nil 时什么都不做
// o 连接池参数
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
