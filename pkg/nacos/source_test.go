package nacos

import (
	"testing"
	"time"

	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

// fakeClient 模拟 nacos config client。
type fakeClient struct {
	config_client.IConfigClient
	content string
	watched []vo.ConfigParam
}

func (f *fakeClient) GetConfig(p vo.ConfigParam) (string, error) { return f.content, nil }
func (f *fakeClient) ListenConfig(p vo.ConfigParam) error {
	f.watched = append(f.watched, p)
	return nil
}
func (f *fakeClient) CancelListenConfig(p vo.ConfigParam) error { return nil }

func TestSourceLoadAndWatch(t *testing.T) {
	fc := &fakeClient{content: "server:\n  http:\n    timeout: 1s\n"}
	src := NewSource(fc, WithDataID("chatroom-user.yaml"), WithGroup("DEFAULT_GROUP"))

	kvs, err := src.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(kvs) != 1 {
		t.Fatalf("want 1 kv, got %d", len(kvs))
	}
	if kvs[0].Key != "chatroom-user.yaml" {
		t.Errorf("key = %q", kvs[0].Key)
	}
	if kvs[0].Format != "yaml" {
		t.Errorf("format = %q, want yaml", kvs[0].Format)
	}

	w, err := src.Watch()
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if len(fc.watched) != 1 {
		t.Fatalf("ListenConfig not called")
	}
	if fc.watched[0].DataId != "chatroom-user.yaml" || fc.watched[0].Group != "DEFAULT_GROUP" {
		t.Errorf("watched = %+v", fc.watched[0])
	}
	if err := w.Stop(); err != nil {
		t.Errorf("Stop: %v", err)
	}
}

// TestWatcher_PublishNeverBlocks OnChange 回调不能被缓冲区满卡死。
func TestWatcher_PublishNeverBlocks(t *testing.T) {
	fc := &fakeClient{content: "v0"}
	src := NewSource(fc, WithDataID("chatroom-user.yaml"), WithGroup("DEFAULT_GROUP"))
	w, err := src.Watch()
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	watcher := w.(*watcher)

	// 连灌 100 次，中间没人读 —— 每次 publish 都必须立刻返回。
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			watcher.publish("v")
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("publish 阻塞了 —— nacos 回调 goroutine 会被卡死")
	}

	// 最新值必须还在通道里。
	kvs, err := watcher.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if string(kvs[0].Value) != "v" {
		t.Errorf("value = %q, want %q", kvs[0].Value, "v")
	}
}

// TestWatcher_PublishKeepsLatestValue 连续变更后，取到的应该是最后一次的值。
func TestWatcher_PublishKeepsLatestValue(t *testing.T) {
	fc := &fakeClient{content: "v0"}
	src := NewSource(fc, WithDataID("chatroom-user.yaml"), WithGroup("DEFAULT_GROUP"))
	w, err := src.Watch()
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	watcher := w.(*watcher)

	// 缓冲区只有 1 —— 塞满之后再来一个应该替换掉旧值。
	watcher.publish("old")
	watcher.publish("new")

	kvs, err := watcher.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if string(kvs[0].Value) != "new" {
		t.Errorf("value = %q, want %q", kvs[0].Value, "new")
	}
}
