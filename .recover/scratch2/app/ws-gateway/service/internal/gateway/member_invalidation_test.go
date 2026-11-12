package gateway

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"chatroom/ws-gateway-service/internal/utils"
)

// TestHandleMemberInvalidation_InvalidatesCache 收到成员变更通知后，
// 对应会话的缓存必须失效 —— 下次广播要重新回源拿最新成员。
func TestHandleMemberInvalidation_InvalidatesCache(t *testing.T) {
	calls := 0
	c := NewMemberCache(MemberFetcherFunc(func(_ context.Context, _ uint64) ([]string, error) {
		calls++
		return []string{"uid-1", "uid-2"}, nil
	}), time.Hour) // TTL 给足长 —— 只有通知能让它失效

	if _, err := c.Get(context.Background(), 7); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if calls != 1 {
		t.Fatalf("回源次数 = %d, want 1", calls)
	}

	payload, err := json.Marshal(&MemberInvalidationPayload{ConversationID: 7})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	HandleMemberInvalidation(c, payload, nil)

	if _, err := c.Get(context.Background(), 7); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if calls != 2 {
		t.Errorf("回源次数 = %d, want 2（通知后要重新回源）", calls)
	}
}

// TestHandleMemberInvalidation_OnlyTargetConversation 受影响的是通知里的那个会话，
// 别的会话的缓存不能被误清。
func TestHandleMemberInvalidation_OnlyTargetConversation(t *testing.T) {
	calls := map[uint64]int{}
	c := NewMemberCache(MemberFetcherFunc(func(_ context.Context, id uint64) ([]string, error) {
		calls[id]++
		return []string{"uid-1"}, nil
	}), time.Hour)

	for _, id := range []uint64{7, 8} {
		if _, err := c.Get(context.Background(), id); err != nil {
			t.Fatalf("Get: %v", err)
		}
	}

	payload, err := json.Marshal(&MemberInvalidationPayload{ConversationID: 7})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	HandleMemberInvalidation(c, payload, nil)

	// 会话 7 重新回源，会话 8 走缓存。
	if _, err := c.Get(context.Background(), 7); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := c.Get(context.Background(), 8); err != nil {
		t.Fatalf("Get: %v", err)
	}

	if calls[7] != 2 {
		t.Errorf("会话 7 回源次数 = %d, want 2", calls[7])
	}
	if calls[8] != 1 {
		t.Errorf("会话 8 回源次数 = %d, want 1（不该被误清）", calls[8])
	}
}

// TestHandleMemberInvalidation_BadPayload 坏载荷不能让订阅协程死掉 ——
// 只记日志，跳过。
func TestHandleMemberInvalidation_BadPayload(t *testing.T) {
	calls := 0
	c := NewMemberCache(MemberFetcherFunc(func(_ context.Context, _ uint64) ([]string, error) {
		calls++
		return []string{"uid-1"}, nil
	}), time.Hour)

	HandleMemberInvalidation(c, []byte("not json"), nil)
	HandleMemberInvalidation(c, []byte(`{"conversation_id":0}`), nil)

	if calls != 0 {
		t.Errorf("回源次数 = %d, want 0", calls)
	}
}

// TestHandleMemberInvalidation_NilCache nil 缓存上调用不能 panic。
func TestHandleMemberInvalidation_NilCache(t *testing.T) {
	HandleMemberInvalidation(nil, []byte(`{"conversation_id":7}`), nil)
}

// TestMemberInvalidationSubscriber_NilRedis 没 redis 时不订阅 ——
// 缓存仍按 TTL 自然过期，不能因为没有通知就崩。
func TestMemberInvalidationSubscriber_NilRedis(t *testing.T) {
	if NewMemberInvalidationSubscriber(nil, nil, nil) != nil {
		t.Error("rdb 为 nil 时该返 nil")
	}
}

// TestMemberInvalidationSubscriber_ReceivesPublishedNotification
// 端到端：conversation 服务 PUBLISH 成员变更通知，网关订阅后缓存失效。
func TestMemberInvalidationSubscriber_ReceivesPublishedNotification(t *testing.T) {
	rdb := newGatewayTestRedis(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calls := 0
	c := NewMemberCache(MemberFetcherFunc(func(_ context.Context, _ uint64) ([]string, error) {
		calls++
		return []string{"uid-1"}, nil
	}), time.Hour)

	if _, err := c.Get(ctx, 900401); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if calls != 1 {
		t.Fatalf("回源次数 = %d, want 1", calls)
	}

	sub := NewMemberInvalidationSubscriber(rdb, c, nil)
	if sub == nil {
		t.Fatal("subscriber 不该是 nil")
	}
	done := make(chan error, 1)
	go func() { done <- sub.Run(ctx) }()
	time.Sleep(200 * time.Millisecond)

	payload, err := json.Marshal(&MemberInvalidationPayload{ConversationID: 900401})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := rdb.Publish(ctx, utils.MemberInvalidatedChannel(), payload).Err(); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// 轮询等缓存失效。
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := c.Get(ctx, 900401); err != nil {
			t.Fatalf("Get: %v", err)
		}
		if calls > 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("缓存没失效")
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run 没返回")
	}
}

// TestMemberInvalidatedChannel_MatchesConversationService 频道名两边必须一致 ——
// 不一致就是静默失联：conversation 服务发了通知，网关永远收不到。
func TestMemberInvalidatedChannel_MatchesConversationService(t *testing.T) {
	if got := utils.MemberInvalidatedChannel(); got != "chatroom:member:invalidated" {
		t.Errorf("频道名 = %q, want %q", got, "chatroom:member:invalidated")
	}
}
