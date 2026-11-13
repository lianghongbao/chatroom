package data

import (
	"time"

	"chatroom/app/ws-gateway/service/internal/conf"

	"github.com/go-kratos/kratos/v3/log"
	"github.com/google/wire"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/types/known/durationpb"
)

// ProviderSet 是 ws-gateway 的 data providers。
// 网关不落库 —— 所以这里只有 Redis 客户端，没有 ent client、没有 repo。
var ProviderSet = wire.NewSet(NewData, NewRedisCmdable, NewRedisClient, NewSender, NewLister, NewRemoteConversationClient, NewMembershipAdapter, NewUnreadAdapter, NewMembersAdapter, NewMemberListerAdapter, NewUserConversationListerAdapter, NewRemoteMessageClient)

// Data 持有 redis 长连接客户端。
type Data struct {
	rdb *redis.Client
}

// NewData 打开 redis 客户端。
// c 配置段
// 返回的清理函数负责关闭客户端。
func NewData(c *conf.Data) (*Data, func(), error) {
	var rdb *redis.Client
	if rc := c.GetRedis(); rc != nil && rc.GetAddr() != "" {
		rdb = redis.NewClient(&redis.Options{
			Network:      rc.GetNetwork(),
			Addr:         rc.GetAddr(),
			ReadTimeout:  durationOf(rc.GetReadTimeout()),
			WriteTimeout: durationOf(rc.GetWriteTimeout()),
		})
	} else {
		log.Warn("redis 未配置，网关将降级（不订阅广播、不报在线状态）")
	}

	cleanup := func() {
		log.Info("closing the data resources")
		if rdb != nil {
			if err := rdb.Close(); err != nil {
				log.Error("failed closing the redis client", "err", err)
			}
		}
	}
	return &Data{rdb: rdb}, cleanup, nil
}

// NewRedisCmdable 暴露共享的 redis.Cmdable。
func NewRedisCmdable(d *Data) redis.Cmdable {
	return d.Redis()
}

// NewRedisClient 暴露共享的 *redis.Client。
// 网关用它订阅 Redis Pub/Sub —— PSubscribe 只在具体类型上有，redis.Cmdable 接口不含订阅方法。
func NewRedisClient(d *Data) *redis.Client {
	return d.RedisClient()
}

// Redis 返回共享 redis client；可能为 nil。
func (d *Data) Redis() redis.Cmdable {
	if d == nil || d.rdb == nil {
		return nil
	}
	return d.rdb
}

// RedisClient 返回具体的 *redis.Client；可能为 nil。
// 跟 Redis() 的区别：PSubscribe 只在 *redis.Client 上有。
func (d *Data) RedisClient() *redis.Client {
	if d == nil {
		return nil
	}
	return d.rdb
}

// durationOf 把 proto duration 转成 time.Duration。nil（未设置）返 0 —— 0 表示不设超时。
func durationOf(d *durationpb.Duration) time.Duration {

	if d == nil {
		return 0
	}
	return d.AsDuration()
}
