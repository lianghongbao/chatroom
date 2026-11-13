package gateway

import (
	"chatroom/pkg/keys"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/redis/go-redis/v9"
)

// Subscriber 订阅 Redis Pub/Sub 上的会话消息频道，把消息推给本实例上的目标连接。
// 用模式订阅（PSUBSCRIBE）而不是逐个会话订阅 —— 后者要处理订阅/退订的并发和漏订。
type Subscriber struct {
	rdb       *redis.Client
	pattern   string
	channel   string
	onMessage func(channel string, payload []byte)
}

// NewSubscriber 构造 Subscriber，onMessage 是收到消息时的回调。
// rdb 为 nil 时返 nil（不订阅）。
func NewSubscriber(rdb *redis.Client, onMessage func(channel string, payload []byte)) *Subscriber {
	if rdb == nil {
		return nil
	}
	return &Subscriber{
		rdb:       rdb,
		pattern:   keys.MessageChannelPattern(),
		onMessage: onMessage,
	}
}

// NewChannelSubscriber 构造 Subscriber，订阅单个频道（不是模式），channel 是频道名。
// 成员失效通知用一个全局频道就够了 —— 变更不频繁，全局频道避免了
// 「订阅/退订并发」和漏订的问题，跟消息广播用模式订阅是同一个理由。
func NewChannelSubscriber(rdb *redis.Client, channel string, onMessage func(channel string, payload []byte)) *Subscriber {
	if rdb == nil {
		return nil
	}
	return &Subscriber{
		rdb:       rdb,
		channel:   channel,
		onMessage: onMessage,
	}
}

// Run 阻塞订阅，直到 ctx 结束。返回 ctx 的错误。
// 订阅 channel 关闭（Redis 重启 / 连接断开）时返回 errSubClosed —— 不是 nil。
// 调用方要靠这个区分「正常结束」和「订阅断了要重连」，两者都返 nil 会让重连循环变成热循环。
func (s *Subscriber) Run(ctx context.Context) error {
	if s == nil || s.rdb == nil {
		return nil
	}
	var sub *redis.PubSub
	if s.channel != "" {
		sub = s.rdb.Subscribe(ctx, s.channel)
	} else {
		sub = s.rdb.PSubscribe(ctx, s.pattern)
	}
	defer func() { _ = sub.Close() }()

	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-ch:
			if !ok {
				return errSubClosed
			}
			if s.onMessage == nil {
				continue
			}
			s.onMessage(msg.Channel, []byte(msg.Payload))
		}
	}
}

// errSubClosed 订阅 channel 关闭了 —— 需要重连。
var errSubClosed = errors.New("redis subscription closed")

// PublishPayload 广播到 Redis 的消息体。字段跟 message 服务 data 层的一致 ——
// 两边靠 JSON 字段名对齐。
type PublishPayload struct {
	MessageID      uint64 `json:"message_id"`
	ConversationID uint64 `json:"conversation_id"`
	SenderID       string `json:"sender_id"`
	Type           uint8  `json:"type"`
	Content        string `json:"content"`
	CreatedAt      int64  `json:"created_at"`
}

// ConversationIDFromChannel 从频道名解析会话 id，channel 是频道名。解析失败返 0。
func ConversationIDFromChannel(channel string) uint64 {
	idx := strings.LastIndex(channel, ":")
	if idx < 0 || idx == len(channel)-1 {
		return 0
	}
	var id uint64
	for _, r := range channel[idx+1:] {
		if r < '0' || r > '9' {
			return 0
		}
		id = id*10 + uint64(r-'0')
	}
	return id
}

// DecodePublishPayload 解析广播体，payload 是广播体原文。
func DecodePublishPayload(payload []byte) (*PublishPayload, error) {
	var p PublishPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	return &p, nil
}
