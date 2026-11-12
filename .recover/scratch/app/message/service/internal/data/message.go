		return nil
	}
	return &biz.Message{
		ID:             po.ID,
		ConversationID: po.ConversationID,
		SenderID:       po.SenderID,
		Type:           biz.MessageType(po.Type),
		Content:        po.Content,
		CreatedAt:      po.CreatedAt,
	}
}

// MessagePublisher biz.MessagePublisher 的 Redis Pub/Sub 实现。
// 每个会话一个频道，网关按会话订阅。Pub/Sub 不持久化，订阅方掉线期间的消息会丢。
type MessagePublisher struct {
	rdb redis.Cmdable
}

// NewMessagePublisher 构造 MessagePublisher。Redis 不可用时返 nil（不广播）。
func NewMessagePublisher(d *Data) *MessagePublisher {
	if d == nil || d.Redis() == nil {
		return nil
	}
	return &MessagePublisher{rdb: d.Redis()}
}

// Publish 把消息发到该会话的频道。
func (p *MessagePublisher) Publish(ctx context.Context, m *biz.Message) error {
	if p == nil || p.rdb == nil {
		return nil
	}
	if m == nil {
		return biz.ErrMessageInvalidArgument
	}
	payload, err := marshalPublishPayload(m)
	if err != nil {
		return err
	}
	return p.rdb.Publish(ctx, messageChannel(m.ConversationID), payload).Err()
}

// messageChannel 会话对应的 Pub/Sub 频道名，与 ws-gateway 订阅的频道名一致。
func messageChannel(conversationID uint64) string {
	return utils.MessageChannel(conversationID)
}

// PublishPayload 广播到 Redis 的消息体，网关直接读内容，不用回查 message 服务。
type PublishPayload struct {
	MessageID      uint64 `json:"message_id"`
	ConversationID uint64 `json:"conversation_id"`
	SenderID       string `json:"sender_id"`
	Type           uint8  `json:"type"`
	Content        string `json:"content"`
	CreatedAt      int64  `json:"created_at"`
}

// marshalPublishPayload 把 biz.Message 折成广播体。
func marshalPublishPayload(m *biz.Message) ([]byte, error) {
	return json.Marshal(&PublishPayload{
		MessageID:      m.ID,
		ConversationID: m.ConversationID,
		SenderID:       m.SenderID,
		Type:           uint8(m.Type),
		Content:        m.Content,
		CreatedAt:      m.CreatedAt.Unix(),
	})
}

// formatUint 把 uint64 转成十进制字符串。消息广播热路径上用 strconv 更快。
func formatUint(v uint64) string {