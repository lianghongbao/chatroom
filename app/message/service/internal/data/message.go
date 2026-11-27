package data

import (
	"context"
	"encoding/json"
	"strconv"

	"chatroom/app/message/service/internal/biz"
	"chatroom/app/message/service/internal/data/ent"
	"chatroom/app/message/service/internal/data/ent/message"

	"chatroom/pkg/keys"
	"github.com/redis/go-redis/v9"
)

// messageRepo biz.MessageRepo 的 ent 实现。
// 表 messages 上挂 (conversation_id, id) 复合索引，覆盖历史消息的排序。
type messageRepo struct {
	data *Data
}

// NewMessageRepo 构造 biz.MessageRepo 的 ent 实现。
// data 共享的数据客户端
func NewMessageRepo(data *Data) biz.MessageRepo {
	return &messageRepo{data: data}
}

// toBizMessage ent.Message PO → biz.Message DO。
// po 为 nil 时返 nil
func toBizMessage(po *ent.Message) *biz.Message {
	if po == nil {
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

// Create 落库一条消息。m 是待存的领域对象，返回带 id 与 created_at 的消息。
func (r *messageRepo) Create(ctx context.Context, m *biz.Message) (*biz.Message, error) {
	po, err := r.data.db.Message.Create().
		SetConversationID(m.ConversationID).
		SetSenderID(m.SenderID).
		SetType(uint8(m.Type)).
		SetContent(m.Content).
		Save(ctx)
	if err != nil {
		return nil, err
	}
	return toBizMessage(po), nil
}

// ListByConversation 拉历史消息，按 id 倒序（新的在前）。返回 (消息列表, 下一页游标)，没有更多时游标为 0。
// conversationID 会话 ID
// cursor 语义是「比这个 id 更旧的」，0 从最新一条开始
// limit 单页条数
func (r *messageRepo) ListByConversation(ctx context.Context, conversationID uint64, cursor uint64, limit int) ([]*biz.Message, uint64, error) {
	q := r.data.db.Message.Query().
		Where(message.ConversationIDEQ(conversationID)).
		Order(ent.Desc(message.FieldID)).
		Limit(limit)
	// cursor=0 表示从最新一条开始；非 0 时只取比它更旧的。
	if cursor > 0 {
		q = q.Where(message.IDLT(cursor))
	}
	pos, err := q.All(ctx)
	if err != nil {
		return nil, 0, err
	}
	msgs := make([]*biz.Message, 0, len(pos))
	for _, po := range pos {
		msgs = append(msgs, toBizMessage(po))
	}
	// 下一页游标 = 本页最后一条（最旧那条）的 id。没有更多时返 0。
	var next uint64
	if len(pos) == limit && len(pos) > 0 {
		next = pos[len(pos)-1].ID
	}
	return msgs, next, nil
}

// ListSince 拉「id 严格大于 sinceID」的消息，按 id 正序（旧的在前）。返回 (消息列表, 最后一条消息的 id, 还有更多吗)。
// conversationID 会话 ID
// sinceID 0 表示从头拉
// limit 单页条数
func (r *messageRepo) ListSince(ctx context.Context, conversationID, sinceID uint64, limit int) ([]*biz.Message, uint64, bool, error) {
	q := r.data.db.Message.Query().
		Where(message.ConversationIDEQ(conversationID)).
		Order(ent.Asc(message.FieldID)).
		Limit(limit + 1) // 多拉一条判断还有没有更多
	if sinceID > 0 {
		q = q.Where(message.IDGT(sinceID))
	}
	pos, err := q.All(ctx)
	if err != nil {
		return nil, 0, false, err
	}
	more := len(pos) > limit
	if more {
		pos = pos[:limit]
	}
	msgs := make([]*biz.Message, 0, len(pos))
	for _, po := range pos {
		msgs = append(msgs, toBizMessage(po))
	}
	var last uint64
	if len(pos) > 0 {
		last = pos[len(pos)-1].ID
	}
	return msgs, last, more, nil
}

// MessagePublisher biz.MessagePublisher 的 Redis Pub/Sub 实现。
// 每个会话一个频道，网关按会话订阅。Pub/Sub 不持久化，订阅方掉线期间的消息会丢。
type MessagePublisher struct {
	rdb redis.Cmdable
}

// NewMessagePublisher 构造 MessagePublisher。
// d 共享的数据客户端
// Redis 不可用时返 nil（不广播）
func NewMessagePublisher(d *Data) *MessagePublisher {
	if d == nil || d.Redis() == nil {
		return nil
	}
	return &MessagePublisher{rdb: d.Redis()}
}

// Publish 把消息发到该会话的频道。m 是要广播的消息。
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
// conversationID 会话 ID
func messageChannel(conversationID uint64) string {
	return keys.MessageChannel(conversationID)
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
// m 要广播的消息
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

// formatUint 把 uint64 转成十进制字符串。v 是待转换的值。
func formatUint(v uint64) string {
	return strconv.FormatUint(v, 10)
}

// 编译期断言：repo 必须满足 biz 定义的接口。
var _ biz.MessageRepo = (*messageRepo)(nil)

// 编译期断言：publisher 必须满足 biz 定义的接口。
var _ biz.MessagePublisher = (*MessagePublisher)(nil)
