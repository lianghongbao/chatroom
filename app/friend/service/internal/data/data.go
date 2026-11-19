package data

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"chatroom/app/friend/service/internal/conf"
	"chatroom/app/friend/service/internal/data/ent"
	"chatroom/pkg/dbpool"

	entsql "entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/schema"
	"github.com/go-kratos/kratos/v3/log"
	_ "github.com/go-sql-driver/mysql"
	"github.com/google/wire"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/types/known/durationpb"
)

// ProviderSet 是 friend 服务的 data providers。
var ProviderSet = wire.NewSet(
	NewData, NewRedisCmdable, NewFriendRepo, NewRemoteUserClient,
	NewFriendAdapter, NewFriendLimiter,
	NewHealthChecker,
)

// NewRedisCmdable 暴露共享的 redis.Cmdable。d 是数据层实例。
func NewRedisCmdable(d *Data) redis.Cmdable {
	return d.Redis()
}

// Data 持有 db / rdb 长连接客户端。
type Data struct {
	db *ent.Client
	// sqlDB 是 db 背后的同一个连接池。两者共用一个池，不额外占连接。
	sqlDB *sql.DB
	rdb   *redis.Client
}

// NewData 打开 db / redis 客户端，返回客户端与清理函数。
// c 是数据层配置；Redis 缺配置时 rdb 为 nil，下游降级。
func NewData(c *conf.Data) (*Data, func(), error) {
	dc := c.GetDatabase()
	if dc == nil {
		return nil, nil, errors.New("data.database 未配置")
	}
	// 自己开 *sql.DB 再交给 ent —— 这样 Data 里的 sqlDB 和 ent 共用同一个池。
	sqlDB, err := sql.Open(dc.GetDriver(), dc.GetSource())
	if err != nil {
		return nil, nil, err
	}
	// 连接池参数 —— database/sql 的默认值在生产上不安全：
	// MaxOpenConns 默认无限，ConnMaxLifetime 默认「永不复用」，
	// 后者在 MySQL 代理重启后表现为零星 500。
	dbpool.Apply(sqlDB, dbpool.Options{
		MaxOpenConns:    int(dc.GetMaxOpenConns()),
		MaxIdleConns:    int(dc.GetMaxIdleConns()),
		ConnMaxLifetime: durationOf(dc.GetConnMaxLifetime()),
		ConnMaxIdleTime: durationOf(dc.GetConnMaxIdleTime()),
	})
	db := ent.NewClient(ent.Driver(entsql.OpenDB(dc.GetDriver(), sqlDB)))
	if dc.GetDebug() {
		db = db.Debug()
	}
	// 表结构对齐：ent 比对现状，只发必要的 ALTER TABLE，把库对齐到本服务
	// ent schema 定义的形状。增量而非重建。
	if dc.GetAutoMigrate() {
		// WithForeignKeys(false) —— 不建外键约束。引用完整性靠应用层保证。
		if err := db.Schema.Create(context.Background(), schema.WithForeignKeys(false)); err != nil {
			db.Close()
			return nil, nil, err
		}
	}

	var rdb *redis.Client
	if rc := c.GetRedis(); rc != nil && rc.GetAddr() != "" {
		rdb = redis.NewClient(&redis.Options{
			Network:      rc.GetNetwork(),
			Addr:         rc.GetAddr(),
			// 共享底座的 Redis 设了 requirepass —— 密码走 env 注入，不进版本库。
			Password:     rc.GetPassword(),
			ReadTimeout:  durationOf(rc.GetReadTimeout()),
			WriteTimeout: durationOf(rc.GetWriteTimeout()),
		})
	} else {
		log.Warn("redis 未配置，登录限频等功能将降级")
	}

	cleanup := func() {
		log.Info("closing the data resources")
		if err := db.Close(); err != nil {
			log.Error("failed closing the database", "err", err)
		}
		if rdb != nil {
			if err := rdb.Close(); err != nil {
				log.Error("failed closing the redis client", "err", err)
			}
		}
	}
	return &Data{db: db, sqlDB: sqlDB, rdb: rdb}, cleanup, nil
}

// Redis 返回共享 redis client；可能为 nil。
func (d *Data) Redis() redis.Cmdable {
	if d == nil || d.rdb == nil {
		return nil
	}
	return d.rdb
}

// durationOf 把 proto duration 转成 time.Duration。
// d 是 proto 里的时长。
func durationOf(d *durationpb.Duration) time.Duration {
	if d == nil {
		return 0
	}
	return d.AsDuration()
}
