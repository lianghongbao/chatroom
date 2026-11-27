package gateway

import (
	"context"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
)

// TestOnlineStatusSurvivesOneOfTwoConnectionsClosed 端到端验证：
// 同一个用户开两条 WS 连接，关掉其中一条，在线状态必须保留。
// 这条测试走的是真实的 handleWS 路径 —— 覆盖 defer 里那次 MarkOffline。
func TestOnlineStatusSurvivesOneOfTwoConnectionsClosed(t *testing.T) {
	f := newFakeOnlineRedis()
	rdb := f.client()
	defer rdb.Close()
	online := NewOnlineReporter(rdb, "instance-a", time.Minute)

	// 模拟 wire.go 里的装配。
	var pool *Pool
	pool = NewPool(func(userID string, c *Connection) {
		if pool != nil && pool.Count(userID) > 0 {
			return
		}
		online.MarkOffline(context.Background(), userID)
	})

	s := NewServer(Config{
		Path:         "/ws",
		InstanceID:   "instance-a",
		PingInterval: time.Second,
		PongWait:     5 * time.Second,
		WriteWait:    time.Second,
	}, pool, &fakeMessageSender{}, &fakeTokenVerifier{uid: "uid-42"}, nil, online, nil)

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"

	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	c1, _, err := dialer.Dial(url+"?token=t", nil)
	if err != nil {
		t.Fatalf("dial c1: %v", err)
	}
	c2, _, err := dialer.Dial(url+"?token=t", nil)
	if err != nil {
		t.Fatalf("dial c2: %v", err)
	}

	// 等两条连接都进池子。
	deadline := time.Now().Add(2 * time.Second)
	for pool.Count("uid-42") < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if pool.Count("uid-42") != 2 {
		t.Fatalf("连接数 = %d, want 2", pool.Count("uid-42"))
	}

	// 关掉第一条。
	c1.Close()

	// 等网关把它摘掉。
	deadline = time.Now().Add(3 * time.Second)
	for pool.Count("uid-42") > 1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if pool.Count("uid-42") != 1 {
		t.Fatalf("关掉一条后连接数 = %d, want 1", pool.Count("uid-42"))
	}
	// 多等一会儿，让 defer 里的清理逻辑跑完。
	time.Sleep(100 * time.Millisecond)

	if _, ok := f.get(onlineKey("uid-42")); !ok {
		t.Errorf("用户还有一条连接连着，在线状态被错删了")
	}

	c2.Close()
}

// fakeOnlineRedis 用 redis 的 Hook 机制拦截命令，模拟一个极小的在线状态存储。
// 不连真实 Redis —— 测试要能离线跑。
type fakeOnlineRedis struct {
	mu     sync.Mutex
	values map[string]string
}

func newFakeOnlineRedis() *fakeOnlineRedis {
	return &fakeOnlineRedis{values: map[string]string{}}
}

// hook 拦截 set / exists / eval。
func (f *fakeOnlineRedis) hook(ctx context.Context, cmd redis.Cmder) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch cmd.Name() {
	case "set":
		args := cmd.Args()
		if len(args) >= 3 {
			f.values[args[1].(string)] = args[2].(string)
		}
		cmd.(*redis.StatusCmd).SetVal("OK")
	case "exists":
		args := cmd.Args()
		if len(args) >= 2 {
			if _, ok := f.values[args[1].(string)]; ok {
				cmd.(*redis.IntCmd).SetVal(1)
				return nil
			}
		}
		cmd.(*redis.IntCmd).SetVal(0)
	case "eval":
		// 模拟「比较并删」：只有值等于实例 ID 才删。
		// eval 的 args 是 [eval, script, numkeys, key..., arg...]
		args := cmd.Args()
		if len(args) >= 5 {
			key, _ := args[3].(string)
			want, _ := args[4].(string)
			if cur, ok := f.values[key]; ok && cur == want {
				delete(f.values, key)
			}
		}
		cmd.(*redis.Cmd).SetVal(int64(1))
	}
	return nil
}

// 实现 redis.Hook —— 进程钩子直接接管命令，不落到网络。
func (f *fakeOnlineRedis) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return nil, redis.Nil
	}
}

func (f *fakeOnlineRedis) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		return f.hook(ctx, cmd)
	}
}

func (f *fakeOnlineRedis) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		for _, cmd := range cmds {
			if err := f.hook(ctx, cmd); err != nil {
				return err
			}
		}
		return nil
	}
}

func (f *fakeOnlineRedis) client() *redis.Client {
	rdb := redis.NewClient(&redis.Options{
		Addr:         "127.0.0.1:1", // 不会真连 —— hook 直接返回
		DialTimeout:  time.Millisecond,
		ReadTimeout:  time.Millisecond,
		WriteTimeout: time.Millisecond,
	})
	rdb.AddHook(f)
	return rdb
}

func (f *fakeOnlineRedis) get(key string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.values[key]
	return v, ok
}

// TestOnlineStatusKeptWhileOtherConnectionRemains
// 同一用户开两条连接，关掉其中一条时在线状态必须保留（另一条还连着）。
func TestOnlineStatusKeptWhileOtherConnectionRemains(t *testing.T) {
	f := newFakeOnlineRedis()
	rdb := f.client()
	defer rdb.Close()
	online := NewOnlineReporter(rdb, "instance-a", time.Minute)

	// 模拟 wire.go 里的装配：pool 带 onClose 回调。
	var pool *Pool
	pool = NewPool(func(userID string, c *Connection) {
		// 还有别的连接就不算离线
		if pool != nil && pool.Count(userID) > 0 {
			return
		}
		online.MarkOffline(context.Background(), userID)
	})

	c1 := NewConnection(nil, "uid-42", "t", 4)
	c2 := NewConnection(nil, "uid-42", "t", 4)
	pool.Add(c1)
	pool.Add(c2)
	online.MarkOnline(context.Background(), "uid-42")

	// 关掉其中一条 —— 走真实的 pool.Remove 路径（含 onClose 回调）。
	pool.Remove(c1)

	if _, ok := f.get(onlineKey("uid-42")); !ok {
		t.Errorf("用户还有一条连接连着，在线状态被错删了")
	}
}

// TestOnlineStatusClearedWhenLastConnectionCloses 最后一条连接断了，
// 在线状态必须清掉。
func TestOnlineStatusClearedWhenLastConnectionCloses(t *testing.T) {
	f := newFakeOnlineRedis()
	rdb := f.client()
	defer rdb.Close()
	online := NewOnlineReporter(rdb, "instance-a", time.Minute)

	var pool *Pool
	pool = NewPool(func(userID string, c *Connection) {
		if pool != nil && pool.Count(userID) > 0 {
			return
		}
		online.MarkOffline(context.Background(), userID)
	})

	c := NewConnection(nil, "uid-42", "t", 4)
	pool.Add(c)
	online.MarkOnline(context.Background(), "uid-42")

	pool.Remove(c)

	if _, ok := f.get(onlineKey("uid-42")); ok {
		t.Errorf("最后一条连接断了，在线状态该被清掉")
	}
}
