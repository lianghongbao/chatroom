package gateway

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// gatewayRedisAddr 返回测试用 Redis 地址。
// 测试环境变量：CHATROOM_TEST_REDIS_ADDR，默认 127.0.0.1:6379。
func gatewayRedisAddr() string {
	if addr := os.Getenv("CHATROOM_TEST_REDIS_ADDR"); addr != "" {
		return addr
	}
	return "127.0.0.1:6379"
}

// newGatewayTestRedis 构造接真实 Redis 的客户端。
// 用 DB 14 —— 测试专用，跟应用的 DB 0 隔开。注意：**Redis Pub/Sub 不按 DB 隔离**
// —— DB 14 上的订阅照样收得到 DB 15 上发的消息。所以「换 DB」挡不住跨包串扰，
// 只能靠频道名区分（见下面的订阅测试）。
// Redis 不可达时 skip。
func newGatewayTestRedis(t *testing.T) *redis.Client {
	t.Helper()

	rdb := redis.NewClient(&redis.Options{
		Addr:         gatewayRedisAddr(),
		DB:           14,
		DialTimeout:  2 * time.Second,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 2 * time.Second,
	})
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		t.Skipf("redis 不可达 (%s): %v", gatewayRedisAddr(), err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// TestSubscriber_ReceivesPublishedMessage 订阅端要收到对应会话频道的消息。
func TestSubscriber_ReceivesPublishedMessage(t *testing.T) {
	rdb := newGatewayTestRedis(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	got := make(chan string, 16)
	sub := NewSubscriber(rdb, func(channel string, payload []byte) {
		got <- channel + "|" + string(payload)
	})
	if sub == nil {
		t.Fatal("subscriber 不该是 nil")
	}

	// 订阅要生效得先跑起来。
	done := make(chan error, 1)
	go func() { done <- sub.Run(ctx) }()
	time.Sleep(200 * time.Millisecond)

	const myChannel = "chatroom:msg:900201"
	const myPayload = `{"message_id":1}`
	if err := rdb.Publish(ctx, myChannel, myPayload).Err(); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// 循环读，直到收到**自己**发的那条。不能只读第一条：生产代码的模式订阅是
	// `chatroom:msg:*`，会收到所有会话频道的消息，而 **Redis Pub/Sub 不按 DB 隔离**
	// —— DB 14 上的订阅照样收得到 DB 15 上发的消息。所以别的包（比如
	// internal/data 的 publisher 测试）并行跑时，会先塞一条别人的消息进来。
	// 跳过它们，只认自己的。
	deadline := time.After(3 * time.Second)
	for {
		select {
		case msg := <-got:
			if msg == myChannel+"|"+myPayload {
				goto received
			}
			// 别人的消息，忽略。
		case <-deadline:
			t.Fatal("没收到订阅消息")
		}
	}

received:
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run 没返回")
	}
}

// TestSubscriber_NilRedisReturnsNil 没 redis 客户端时返 nil（不订阅）。
func TestSubscriber_NilRedisReturnsNil(t *testing.T) {
	if NewSubscriber(nil, nil) != nil {
		t.Error("rdb 为 nil 时该返 nil")
	}
}

// TestSubscriber_NilReceiverRunSafe nil 订阅器上调 Run 不能 panic。
func TestSubscriber_NilReceiverRunSafe(t *testing.T) {
	var s *Subscriber
	if err := s.Run(context.Background()); err != nil {
		t.Errorf("err = %v, want nil", err)
	}
}

// TestOnlineReporter_MarkOnlineAndQuery 标记在线后要能查到。
func TestOnlineReporter_MarkOnlineAndQuery(t *testing.T) {
	rdb := newGatewayTestRedis(t)
	ctx := context.Background()

	const userID = "uid-900301"
	t.Cleanup(func() { _ = rdb.Del(ctx, onlineKey(userID)).Err() })

	r := NewOnlineReporter(rdb, "instance-a", time.Minute)
	r.MarkOnline(ctx, userID)

	if !r.IsOnline(ctx, userID) {
		t.Error("该是在线状态")
	}
	// 值应该是实例 ID。
	got, err := rdb.Get(ctx, onlineKey(userID)).Result()
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != "instance-a" {
		t.Errorf("值 = %q, want %q", got, "instance-a")
	}
}

// TestOnlineReporter_MarkOnlineSetsTTL 在线状态要有 TTL —— 实例挂了之后
// 状态要能自动过期。
func TestOnlineReporter_MarkOnlineSetsTTL(t *testing.T) {
	rdb := newGatewayTestRedis(t)
	ctx := context.Background()

	const userID = "uid-900302"
	t.Cleanup(func() { _ = rdb.Del(ctx, onlineKey(userID)).Err() })

	r := NewOnlineReporter(rdb, "instance-a", 30*time.Second)
	r.MarkOnline(ctx, userID)

	ttl, err := rdb.TTL(ctx, onlineKey(userID)).Result()
	if err != nil {
		t.Fatalf("ttl: %v", err)
	}
	if ttl <= 0 || ttl > 30*time.Second {
		t.Errorf("ttl = %v, want (0, 30s]", ttl)
	}
}

// TestOnlineReporter_MarkOfflineClearsStatus 标记离线后状态要被清掉。
func TestOnlineReporter_MarkOfflineClearsStatus(t *testing.T) {
	rdb := newGatewayTestRedis(t)
	ctx := context.Background()

	const userID = "uid-900303"
	t.Cleanup(func() { _ = rdb.Del(ctx, onlineKey(userID)).Err() })

	r := NewOnlineReporter(rdb, "instance-a", time.Minute)
	r.MarkOnline(ctx, userID)
	r.MarkOffline(ctx, userID)

	if r.IsOnline(ctx, userID) {
		t.Error("该是离线状态")
	}
}

// TestOnlineReporter_MarkOfflineKeepsOtherInstance 用户同时连着另一个实例时，
// 本实例下线不能把状态删掉 —— 人还在线。
func TestOnlineReporter_MarkOfflineKeepsOtherInstance(t *testing.T) {
	rdb := newGatewayTestRedis(t)
	ctx := context.Background()

	const userID = "uid-900304"
	t.Cleanup(func() { _ = rdb.Del(ctx, onlineKey(userID)).Err() })

	// 先由另一个实例标记在线。
	other := NewOnlineReporter(rdb, "instance-b", time.Minute)
	other.MarkOnline(ctx, userID)

	// 本实例下线 —— 比较并删会发现值不是自己，不删。
	mine := NewOnlineReporter(rdb, "instance-a", time.Minute)
	mine.MarkOffline(ctx, userID)

	if !mine.IsOnline(ctx, userID) {
		t.Error("别的实例还在线，不该被清掉")
	}
}

// TestOnlineReporter_NilRedisDegrades 没 redis 时降级 —— 不能 panic。
func TestOnlineReporter_NilRedisDegrades(t *testing.T) {
	r := NewOnlineReporter(nil, "instance-a", time.Minute)
	ctx := context.Background()

	r.MarkOnline(ctx, "uid-1")
	r.MarkOffline(ctx, "uid-1")
	if r.IsOnline(ctx, "uid-1") {
		t.Error("没 redis 时该是不在线")
	}
}

// TestOnlineReporter_NilReceiverSafe nil 上报器上调用不能 panic。
func TestOnlineReporter_NilReceiverSafe(t *testing.T) {
	var r *OnlineReporter
	ctx := context.Background()

	r.MarkOnline(ctx, "uid-1")
	r.MarkOffline(ctx, "uid-1")
	if r.IsOnline(ctx, "uid-1") {
		t.Error("nil 上报器该是不在线")
	}
}

// TestOnlineKey_Format key 格式固定。
func TestOnlineKey_Format(t *testing.T) {
	if got := onlineKey("uid-42"); got != "online:uid-42" {
		t.Errorf("key = %q, want %q", got, "online:uid-42")
	}
}
