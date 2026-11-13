package data

import (
	"context"
	"encoding/json"

	"chatroom/pkg/keys"

	"github.com/redis/go-redis/v9"
)

// MemberInvalidationPublisher biz.MemberInvalidationPublisher 的 Redis Pub/Sub 实现。
// 成员变了就通知 ws-gateway 失效本地成员缓存；通知会丢，只影响时效。
type MemberInvalidationPublisher struct {
	rdb redis.Cmdable
}

// NewMemberInvalidationPublisher 构造 publisher。
// d 是 data 客户端
// Redis 不可用时返 nil。
func NewMemberInvalidationPublisher(d *Data) *MemberInvalidationPublisher {
	if d == nil || d.Redis() == nil {
		return nil
	}
	return &MemberInvalidationPublisher{rdb: d.Redis()}
}

// Publish 通知「会话 X 的成员变了」。conversationID 是会话 ID。
func (p *MemberInvalidationPublisher) Publish(ctx context.Context, conversationID uint64) error {
	if p == nil || p.rdb == nil {
		return nil
	}
	payload, err := json.Marshal(&MemberInvalidationPayload{ConversationID: conversationID})
	if err != nil {
		return err
	}
	return p.rdb.Publish(ctx, keys.MemberInvalidatedChannel(), payload).Err()
}

// MemberInvalidationPayload 成员变更通知的载荷，网关按 JSON 字段名解析。
type MemberInvalidationPayload struct {
	ConversationID uint64 `json:"conversation_id"`
}
