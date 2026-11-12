package gateway

import (
	"context"
	"encoding/json"
	"log/slog"

	"chatroom/pkg/keys"

	"github.com/redis/go-redis/v9"
)

// MemberInvalidationPayload 成员变更通知的载荷。
// 跟 conversation 服务 data 层的一致 —— 两边靠 JSON 字段名对齐。
type MemberInvalidationPayload struct {
	ConversationID uint64 `json:"conversation_id"`
}

// InvalidatorSubscriber 订阅成员失效通知的订阅器。
//
// 单独一个类型（而不是直接用 *Subscriber）—— wire 靠类型区分 provider，
// 两个 *Subscriber provider 会撞车。
type InvalidatorSubscriber struct {
	*Subscriber
}

// NewMemberInvalidationSubscriber 构造订阅成员失效通知的订阅器。
//
// rdb 为 nil 时返 nil（不订阅）—— 缓存仍按 TTL 自然过期。
func NewMemberInvalidationSubscriber(rdb *redis.Client, members *MemberCache, logger *slog.Logger) *InvalidatorSubscriber {
	if rdb == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &InvalidatorSubscriber{Subscriber: NewChannelSubscriber(rdb, keys.MemberInvalidatedChannel(), func(_ string, payload []byte) {
		HandleMemberInvalidation(members, payload, logger)
	})}
}

// HandleMemberInvalidation 处理一条成员变更通知：让对应会话的成员缓存失效。
//
// 解析失败只记日志 —— 一条坏消息不该把整个订阅搞死。
func HandleMemberInvalidation(members *MemberCache, payload []byte, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	var p MemberInvalidationPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		logger.WarnContext(context.Background(), "成员变更通知解析失败", "err", err)
		return
	}
	if p.ConversationID == 0 {
		return
	}
	members.Invalidate(p.ConversationID)
	logger.InfoContext(context.Background(), "成员缓存已失效", "conversation_id", p.ConversationID)
}
