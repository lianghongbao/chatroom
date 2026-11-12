package gateway

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

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

// TestBug_HandleWSDeferMarksOfflineEvenWhenOtherConnsRemain
// 同一个用户有两条连接，关掉其中一条时，handleWS 的 defer 里无条件
// MarkOffline —— 把「在线」状态错删了。用户其实还在线（另一条连接还连着）。
//
// 这是审查发现的真 bug，用来锁定行为。
func TestBug_HandleWSDeferMarksOfflineEvenWhenOtherConnsRemain(t *testing.T) {
	f := newFakeOnlineRedis()
	rdb := f.client()
	defer rdb.Close()
	online := NewOnlineReporter(rdb, "instance-a", time.Minute)

	// 模拟 wire.go 里的装配：pool 带 onClose 回调。
	var pool *Pool
	pool = NewPool(func(userID string, c *Connection) {
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

	// 关掉其中一条 —— 模拟 handleWS 的 defer：Remove + 无条件 MarkOffline
	pool.Remove(c1)
	online.MarkOffline(context.Background(), "uid-42") // ← 这一行没有条件判断

	if _, ok := f.get(onlineKey("uid-42")); !ok {
		t.Errorf("复现了：用户还有一条连接连着，在线状态被错删了")
	}
}
