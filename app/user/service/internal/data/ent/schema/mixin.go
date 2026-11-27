package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"entgo.io/ent/schema/mixin"
	"github.com/google/uuid"
)

// UIDMixin 给 schema 加上应用侧生成的 UUIDv7 字符串主键。
//
// 用 string 而不是 uuid.UUID：biz / proto 层按 string 传，避免每层边界
// 做一次 uuid.UUID ↔ string 转换。DB 列是 CHAR(36)。
type UIDMixin struct {
	mixin.Schema
}

// UIDMixin 的字段。
func (UIDMixin) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").
			MaxLen(36).
			DefaultFunc(newUIDString).
			Immutable().
			Comment("主键, UUIDv7 字符串"),
	}
}

// newUIDString 生成应用侧 UUIDv7 字符串。
func newUIDString() string {
	return uuid.Must(uuid.NewV7()).String()
}

// TimeMixin 给 schema 加上审计时间戳。
type TimeMixin struct {
	mixin.Schema
}

// TimeMixin 的字段。
func (TimeMixin) Fields() []ent.Field {
	return []ent.Field{
		field.Time("created_at").Immutable().Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

// TimeMixin 的索引。
func (TimeMixin) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("updated_at"),
	}
}
