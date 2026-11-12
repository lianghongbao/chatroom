package data

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"chatroom/pkg/keys"
	"chatroom/pkg/testutil"

	"github.com/redis/go-redis/v9"
)

// newTestMemberPublisher 构造接真实 Redis 的 publisher。
func newTestMemberPublisher(t *testing.T) (*MemberInvalidationPublisher, *redis.Client) {
	t.Helper()
	rdb := testutil.NewRedis(t)
	return &MemberInvalidationPublisher{rdb: rdb}, rdb
}

// TestMemberInvalidationPublisher_PublishesToChannel 成员变更通知要发到
// `chatroom:member:invalidated` 频道上，载荷带会话 ID。
func TestMemberInvalidationPublisher_PublishesToChannel(t *testing.T) {
	p, rdb := newTestMemberPublisher(t)
	ctx := context.Background()

	const convID = 900501
	channel := keys.MemberInvalidatedChannel()

	sub := rdb.Subscribe(ctx, channel)
	t.Cleanup(func() { _ = sub.Close() })
	if _, err := sub.Receive(ctx); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if err := p.Publish(ctx, convID); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	ch := sub.Channel()
	select {
	case got := <-ch:
		var payload MemberInvalidationPayload
		if err := json.Unmarshal([]byte(got.Payload), &payload); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if payload.ConversationID != convID {
			t.Errorf("conversation id = %d, want %d", payload.ConversationID, convID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("没收到通知")
	}
}

// TestMemberInvalidationPublisher_NilPublisher nil publisher 上调用不能 panic。
func TestMemberInvalidationPublisher_NilPublisher(t *testing.T) {
	var p *MemberInvalidationPublisher
	if err := p.Publish(context.Background(), 7); err != nil {
		t.Errorf("err = %v, want nil", err)
	}
}

// TestNewMemberInvalidationPublisher_NilData 没有 redis 时返 nil ——
// 通知不发，缓存按 TTL 自然过期。
func TestNewMemberInvalidationPublisher_NilData(t *testing.T) {
	if NewMemberInvalidationPublisher(nil) != nil {
		t.Error("data 为 nil 时该返 nil")
	}
}

// TestMemberInvalidatedChannel_Format 频道名格式固定 —— 两边服务必须一致，
// 不一致就是静默失联。
func TestMemberInvalidatedChannel_Format(t *testing.T) {
	if got := keys.MemberInvalidatedChannel(); got != "chatroom:member:invalidated" {
		t.Errorf("channel = %q, want %q", got, "chatroom:member:invalidated")
	}
}
