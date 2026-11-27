package testutil

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisAddrEnv 测试用 Redis 地址的环境变量名
const RedisAddrEnv = "CHATROOM_TEST_REDIS_ADDR"

// RedisAddr 返回测试用 Redis 地址；没配环境变量时用默认本地地址。
func RedisAddr() string {
	if addr := os.Getenv(RedisAddrEnv); addr != "" {
		return addr
	}
	return "127.0.0.1:6379"
}

// NewRedis 构造一个接测试 Redis 的客户端。用 DB 15，Redis 不可达时 skip。
func NewRedis(t *testing.T) *redis.Client {
	t.Helper()

	rdb := redis.NewClient(&redis.Options{
		Addr:         RedisAddr(),
		DB:           15,
		DialTimeout:  2 * time.Second,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 2 * time.Second,
	})
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		t.Skipf("redis 不可达 (%s): %v", RedisAddr(), err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}
