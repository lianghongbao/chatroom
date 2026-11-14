package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// User 用户表。
//
// 主键是 UIDMixin 生成的 UUIDv7 字符串 —— 全链路（JWT sub、WS 协议、
// Redis key、跨服务调用）都用这个字符串标识用户，不暴露自增序号。
type User struct {
	ent.Schema
}

// User 的表注解。
func (User) Annotations() []schema.Annotation {
	// 把字段 Comment 同步写到 MySQL DDL 的列备注里, 便于运维在
	// INFORMATION_SCHEMA.COLUMNS 里直接核对语义。
	return []schema.Annotation{
		entsql.WithComments(true),
	}
}

// User 混入的字段。
func (User) Mixin() []ent.Mixin {
	return []ent.Mixin{
		UIDMixin{},
	}
}

// User 的字段。
func (User) Fields() []ent.Field {
	return []ent.Field{
		field.String("username").NotEmpty().Comment("登录用户名, 唯一"),
		field.String("nickname").Default("").Comment("展示昵称"),
		field.String("avatar").Default("").Comment("头像 URL"),
		// 存 bcrypt 哈希后的字节串 (60 字节定长)。biz.Register 用bcrypt.DefaultCost 生成
		field.String("password_hash").NotEmpty().Sensitive().Comment("bcrypt 哈希, 60 字节"),
		field.Time("created_at").Immutable().Default(time.Now).Comment("注册时间"),
		// 邮箱。不加唯一索引、不做格式校验 —— 目前只是个备注字段，
		// 将来接"验证码改密 / 找回密码"时再视业务收紧。
		field.String("email").Optional().Nillable().Comment("邮箱, 可选, 不唯一"),
		// 手机号。加好友时用它找人，所以必须全局唯一 —— 否则同一个号对应多个账号，
		// 按号查找时说不清是哪一个。NULL 不参与唯一索引（MySQL 语义），未填手机号的账号不受约束。
		field.String("phone").Optional().Nillable().Comment("手机号, 可选, 全局唯一"),
		// 是否对未关注 / 未加好友的陌生人可见。GetUserByID 路径会用此字段判断是否折叠成 NOT_FOUND，防止社工库枚举 ID 抓资料。
		field.Bool("is_visible_to_stranger").Default(false).Comment("是否对陌生人可见"),
		// 账号状态：1 正常 0 禁用。禁用用户不能登录（Login 拦截），已签发的 access token
		// 在 TTL 内仍然有效（吊销走 auth 黑名单，不靠这一列）。
		field.Uint8("status").Default(1).Comment("账号状态 1=正常 0=禁用"),
	}
}

// User 的索引。
func (User) Indexes() []ent.Index {
	return []ent.Index{
		// username 唯一, 给 FindByUsername 用, 同时兜底 Register 并发竞态。
		index.Fields("username").Unique(),
		// phone 唯一, 给按手机号查找用, 同时兜住「两个账号填了同一个手机号」的并发竞态。
		// NULL 不参与唯一索引 —— 没填手机号的账号可以有很多个。
		index.Fields("phone").Unique(),
	}
}
