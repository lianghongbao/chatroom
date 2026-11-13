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

// 会话表。对应 PRD §5.3。
// 一条记录 = 一个会话（单聊或群聊）。
//   - type=1 单聊：固定两个成员，由 member_ids 唯一确定，重复创建复用同一条。
//   - type=2 群聊：成员可增减，owner_id 指向群主。
//
// 成员本身存在 conversation_members 表，本表只存会话自身的元信息。
type Conversation struct {
	ent.Schema
}

// Annotations of the Conversation.
func (Conversation) Annotations() []schema.Annotation {
	// 把字段 Comment 同步写到 MySQL DDL 的列备注里，便于运维核对语义。
	return []schema.Annotation{
		entsql.WithComments(true),
	}
}

// Fields of the Conversation.
func (Conversation) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id").Comment("主键, 自增"),
		// 1=单聊 2=群聊
		field.Uint8("type").Default(1).Comment("会话类型 1=单聊 2=群聊"),
		// 群名。单聊为空（前端用对方昵称展示）。
		field.String("name").Default("").MaxLen(128).Comment("群名, 单聊为空, ≤ 128 字符"),
		// 群主 uid。单聊为 0。
		// 群主 uid（UUIDv7 字符串）。单聊为空串。
		field.String("owner_id").Default("").MaxLen(36).Comment("群主 uid (UUIDv7 字符串), 单聊为空串"),
		// 单聊的成员对键，形如 "uid1,uid2"（两个 uid 升序拼接）。
		// 群聊为 NULL。这列的唯一索引是「单聊唯一」的数据库级约束 ——
		// 并发建同一对成员的单聊时，第二个请求被唯一索引挡下。
		// MySQL 唯一索引允许存在多个 NULL，群聊之间不会互相冲突。
		// 长度按 UUIDv7 字符串算：两个 uid 各 36 字符 + 一个逗号 = 73。
		field.String("pair_key").Optional().Nillable().MaxLen(73).Comment("单聊成员对键 'uid1,uid2', 群聊为 NULL"),
		field.Time("created_at").Immutable().Default(time.Now).Comment("创建时间"),
		// 最后活动时间。成员变动 / 改名 / 有新消息时更新，会话列表按它倒序。
		// SchemaType(TimeMicro) 让 MySQL 列是 DATETIME(6)，
		// 同一秒内建的多个会话才能分出先后。
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).
			SchemaType(map[string]string{"mysql": "datetime(6)"}).Comment("最后活动时间"),
	}
}

// Indexes of the Conversation.
func (Conversation) Indexes() []ent.Index {
	return []ent.Index{
		// 会话列表按最后活动时间倒序，updated_at 走索引。
		index.Fields("updated_at"),
		// 群主维度查"我创建的群聊"。
		index.Fields("owner_id"),
		// 单聊唯一约束：一对成员只能有一个单聊。
		//
		// 群聊的 pair_key 是 NULL，MySQL 唯一索引不约束 NULL，
		// 所以群聊之间不受影响。
		index.Fields("pair_key").Unique(),
	}
}

// Edges of the Conversation.
func (Conversation) Edges() []ent.Edge {
	return []ent.Edge{
		// 到成员的边。会话列表要按"我参与的会话"过滤，靠这条边生成
		// HasMembersWith 子查询（走 conversation_members 的索引）。
		edge.To("members", ConversationMember.Type),
	}
}
