package gateway

import (
	"context"
	"encoding/json"
	"log/slog"

	"chatroom/pkg/keys"

	"github.com/redis/go-redis/v9"
)

// MemberInvalidationPayload 成员变更通知的载荷，跟 conversation 服务 data 层靠 JSON 字段名对齐。
type MemberInvalidationPayload struct {
	ConversationID uint64 `json:"conversation_id"`
}

// InvalidatorSubscriber 订阅成员失效通知的订阅器（单独一个类型，wire 靠类型区分 provider）。
type InvalidatorSubscriber struct {
	*Subscriber
}

// NewMemberInvalidationSubscriber 构造成员失效通知的订阅器。
// rdb Redis 客户端，为 nil 时返 nil（不订阅）
// members 要失效的成员缓存
// logger 日志器，为 nil 时用默认 logger
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

// HandleMemberInvalidation 处理一条成员变更通知，让对应会话的成员缓存失效。
// members 成员缓存
// payload 通知原文
// logger 日志器，为 nil 时用默认 logger
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
