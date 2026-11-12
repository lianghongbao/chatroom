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
//
// 一条记录 = "某人在某会话里"。unread_count 记该成员在这个会话里的
// 未读消息数（消息服务 P4 落地写入逻辑；P3 先建好字段）。
//
// 下面的 edge 会在 DDL 里生成外键约束，但 migrate 时用
// schema.WithForeignKeys(false) 关掉了 —— 引用完整性靠 repo 在事务里保证，
// 成员表可以不被父表约束绑住。详见 README「表结构变更」。
type ConversationMember struct {
	ent.Schema
}

// Annotations of the ConversationMember.
func (ConversationMember) Annotations() []schema.Annotation {
	// 把字段 Comment 同步写到 MySQL DDL 的列备注里，便于运维核对语义。
	return []schema.Annotation{
		entsql.WithComments(true),
	}
}

// Fields of the ConversationMember.
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

// Indexes of the ConversationMember.
func (ConversationMember) Indexes() []ent.Index {
	return []ent.Index{
		// 同一会话内不能有重复成员。also serves 幂等写入。
		index.Fields("conversation_id", "user_id").Unique(),
		// "我参与了哪些会话" —— 会话列表的主查询路径。
		index.Fields("user_id", "conversation_id"),
	}
}

// Edges of the ConversationMember.
func (ConversationMember) Edges() []ent.Edge {
	return []ent.Edge{
		// 到会话的边。与 Conversation.Members 构成一对多。
		//
		// edge.Field("conversation_id") 把边绑到已有的标量外键列上，
		// 而不是让 ent 再生成一列同名外键（那会与标量字段的方法名冲突）。
		// Required() 与 conversation_id 字段的"非可选"保持一致 ——
		// ent 要求 edge 与其绑定字段的可选性匹配。
		edge.From("conversation", Conversation.Type).
			Ref("members").
			Unique().
			Required().
			Field("conversation_id"),
	}
}
