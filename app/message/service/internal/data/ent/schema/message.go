package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// 消息表。对应 PRD §5.5。
// 一条记录 = 一条消息。消息不可编辑、不可删除 —— 要撤回也得留痕。
// 不建外键：conversation_id 指向的会话可能已解散，消息保留着（历史可查）。
type Message struct {
	ent.Schema
}

// Annotations of the Message.
func (Message) Annotations() []schema.Annotation {
	// 把字段 Comment 同步写到 MySQL DDL 的列备注里，便于运维核对语义。
	return []schema.Annotation{
		entsql.WithComments(true),
	}
}

// Fields of the Message.
func (Message) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id").Comment("主键, 自增"),
		field.Uint64("conversation_id").Positive().Comment("会话 id"),
		field.String("sender_id").MaxLen(36).Comment("发送者 uid (UUIDv7 字符串)"),
		// 1=文本 2=图片（图片存 URL）
		field.Uint8("type").Default(1).Comment("消息类型 1=文本 2=图片"),
		// 消息内容。文本消息存原文，图片消息存图片 URL。
		//
		// TEXT 而不是 VARCHAR —— 聊天内容长度不可控，而且不会拿它做等值查询。
		field.String("content").Comment("消息内容/图片 URL"),
		field.Time("created_at").Immutable().Default(time.Now).Comment("发送时间"),
	}
}

// Indexes of the Message.
func (Message) Indexes() []ent.Index {
	return []ent.Index{
		// 历史消息的主查询路径：某个会话的消息，按 id 倒序。
		// (conversation_id, id) 复合索引天然覆盖 conversation_id + id 的排序，
		// 游标分页直接走它，不用回表排序。
		index.Fields("conversation_id", "id"),
		// 按发送者查（"我发过的消息"）。会话内查询已经够用，这条留给以后。
		index.Fields("sender_id"),
	}
}
