package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// 会话成员表。对应 PRD §5.4。
// 一条记录 = "某人在某会话里"。unread_count 记该成员在这个会话里的未读消息数。
// migrate 时用 schema.WithForeignKeys(false) 关掉外键约束，
// 引用完整性靠 repo 在事务里保证。
type ConversationMember struct {
	ent.Schema
}

// ConversationMember 的表注解。
func (ConversationMember) Annotations() []schema.Annotation {
	// 把字段 Comment 同步写到 MySQL DDL 的列备注里，便于运维核对语义。
	return []schema.Annotation{
		entsql.WithComments(true),
	}
}

// ConversationMember 的字段。
func (ConversationMember) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id").Comment("主键, 自增"),
		field.Uint64("conversation_id").Positive().Comment("会话 id"),
		field.String("user_id").MaxLen(36).Comment("成员 uid"),
		// 该成员在这个会话里的未读消息数。消息服务上线后由它维护。
		field.Int32("unread_count").Default(0).Comment("未读消息数"),
		field.Time("joined_at").Immutable().Default(time.Now).Comment("加入时间"),
	}
}

// ConversationMember 的索引。
func (ConversationMember) Indexes() []ent.Index {
	return []ent.Index{
		// 同一会话内不能有重复成员。also serves 幂等写入。
		index.Fields("conversation_id", "user_id").Unique(),
		// "我参与了哪些会话" —— 会话列表的主查询路径。
		index.Fields("user_id", "conversation_id"),
	}
}

// ConversationMember 的关联边。
func (ConversationMember) Edges() []ent.Edge {
	return []ent.Edge{
		// 到会话的边。与 Conversation.Members 构成一对多。
		// edge.Field("conversation_id") 把边绑到已有的标量外键列上，
		// 避免 ent 再生成一列同名外键。
		edge.From("conversation", Conversation.Type).
			Ref("members").
			Unique().
			Required().
			Field("conversation_id"),
	}
}
