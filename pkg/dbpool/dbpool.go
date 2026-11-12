// Package dbpool 定义数据库连接池的参数与默认值。
//
// 抽在 pkg 里是因为这几个值**所有服务必须一致** —— 连接池是
// 跟 MySQL 实例共享的资源，每个服务各配各的很容易配出
// 「总量刚好超过 max_connections」这种事故。
//
// 为什么不能留 database/sql 的默认值：
//
//   - MaxOpenConns 默认 0 = **无限**。一个服务扩到 10 个副本
//     （HPA）就是 10 个无限池，足以打爆 MySQL 的 max_connections
//     （默认 151）。
//   - MaxIdleConns 默认 2。高并发下连接被反复建立/销毁，
//     MySQL 侧看到的是连接数剧烈抖动。
//   - ConnMaxLifetime 默认 0 = **永不复用**。MySQL 代理重启或
//     主从切换后，池里的老连接已经死了，表现为「偶发 500」，
//     极难排查 —— 因为重试一次就好了。
package dbpool

import (
	"database/sql"
	"time"
)

// 连接池默认值。
//
// 这几个值是按「MySQL 默认 max_connections=151，5 个服务共库，
// 每服务最多 10 副本」倒推的：10 副本 × 10 连接 = 100 连接/服务。
// 调大 MaxOpenConns 前先确认 MySQL 的 max_connections 够用 ——
// 连接数是所有服务共享的池子，不是每个服务各有一份。
const (
	// DefaultMaxOpenConns 单个进程最多持有多少条连接。
	DefaultMaxOpenConns = 10
	// DefaultMaxIdleConns 空闲连接池大小。
	//
	// 略小于 MaxOpenConns：留一点余量给突发，但不囤太多 ——
	// 囤着的空闲连接也是占着 MySQL 的连接额度。
	DefaultMaxIdleConns = 5
	// DefaultConnMaxLifetime 连接的最长存活时间。
	//
	// 到期就还掉重建 —— 保证连接不会比 MySQL 侧的空闲超时更长寿。
	DefaultConnMaxLifetime = 30 * time.Minute
	// DefaultConnMaxIdleTime 空闲连接的最长保留时间。
	DefaultConnMaxIdleTime = 5 * time.Minute
)

// Options 是连接池参数。
//
// 每一项 <= 0 时用对应的默认值 —— 配置漏填不等于「不要限制」。
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

// Resolve 补齐缺省值。
//
// 每一项 <= 0 都用默认值 —— **配置漏填不等于「不要限制」**。
// 数据库/sql 的默认值在生产上是危险的（见包注释），所以这里一律往回收紧。
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
//
// 调用前 db 必须已经建好（sql.Open 就行 —— 它不真连库）。
// db 为 nil 时什么都不做：连接池没初始化的时候不该 panic，
// 健康检查那边会把它报成「依赖不可用」。
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
