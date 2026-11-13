package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// 关系语义: 一条记录代表 "user_id 对 friend_id 提过申请 / 已是好友"。
// user_id 是发起方, friend_id 是目标方, 由 service 层根据 caller_id
// 决定哪条记录是 "正向"、哪条是 "反向"。
//
// 状态机 (status 字段, 0/1/2 沿用 PRD §5.2):
//
//	0 PENDING  - 申请待确认
//	1 ACCEPTED - 已是好友
//	2 REJECTED - 已拒绝 (终态, 可重新申请)
type Friend struct {
	ent.Schema
}

// Annotations of the Friend.
func (Friend) Annotations() []schema.Annotation {
	// 把字段的 Comment 同步写到 MySQL DDL 的列备注里 (auto_migrate 时
	// 由 ent 编译成 ALTER TABLE ... MODIFY COLUMN ... COMMENT '...')，
	// 便于运维 / DBA 在 INFORMATION_SCHEMA.COLUMNS 里直接核对语义。
	return []schema.Annotation{
		entsql.WithComments(true),
	}
}

// Fields of the Friend.
func (Friend) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id").
			Comment("主键, uint64 自增"),
		field.String("user_id").
			MaxLen(36).
			Comment("发起方 uid (UUIDv7 字符串), Register/Apply 路径写入"),
		field.String("friend_id").
			MaxLen(36).
			Comment("目标方 uid (UUIDv7 字符串), Accept/Reject 路径不更新此字段 (记录归属不变)"),
		// 单聊对键，形如 "uid1,uid2"（两个 uid 升序拼接）。与 user_id/friend_id
		// 一起构成复合唯一索引 —— 同一对用户只能有一条关系记录，
		// 不管是谁先发起的申请。
		field.String("pair_key").
			MaxLen(80).
			Comment("好友对键 'uid1,uid2', 归一化后拼接"),
		field.String("message").
			Default("").
			MaxLen(200).
			Comment("申请附言, 0..200 字符"),
		field.Uint8("status").
			Default(0).
			Comment("关系状态: 0=PENDING, 1=ACCEPTED, 2=REJECTED (与 biz.FriendStatus 对齐)"),
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			Comment("记录创建时间, 建表后不变"),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			Comment("最近一次状态变更时间, PENDING->ACCEPTED/REJECTED 时刷新"),
	}
}

// Indexes of the Friend.
func (Friend) Indexes() []ent.Index {
	return []ent.Index{
		// 复合唯一: 同一对 (user_id, friend_id) 只能有一条记录,
		// 并发 Apply 唯一索引兜底 (Register 路径同样的设计)。
		index.Fields("user_id", "friend_id").Unique(),
		// pair_key 唯一: 归一化后 "a,b" 和 "b,a" 落到同一个键 ——
		// 反向并发申请（A 申请 B 同时 B 申请 A）时,上面那个复合索引挡不住
		// (方向不同),真正的仲裁者是这个唯一索引。
		// 缺了它,反向并发会建出两条互相矛盾的关系记录。
		index.Fields("pair_key").Unique(),
		// 反向查询: "我被谁申请了" / Accept/Reject 阶段按 friend_id 找
		// 待确认的记录。
		index.Fields("friend_id"),
		// 列出 "我的好友" 走 user_id 索引, 按 id 排序即最近建立的关系在前。
		index.Fields("user_id", "id"),
	}
}
