package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"entgo.io/ent/schema/mixin"
	"github.com/google/uuid"
)

// UIDMixin adds the standard application-generated UUIDv7 string primary key.
//
// 用 string 而不是 uuid.UUID：biz / proto 层按 string 传，避免每层边界
// 做一次 uuid.UUID ↔ string 转换。DB 列是 CHAR(36)。
type UIDMixin struct {
	mixin.Schema
}

// Fields of the UIDMixin.
func (UIDMixin) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").
			MaxLen(36).
			DefaultFunc(newUIDString).
			Immutable().
			Comment("主键, UUIDv7 字符串"),
	}
}

// newUIDString returns an application-generated UUIDv7 as a string.
func newUIDString() string {
	return uuid.Must(uuid.NewV7()).String()
}

// TimeMixin adds standard audit timestamps to schemas.
type TimeMixin struct {
	mixin.Schema
}

// Fields of the TimeMixin.
func (TimeMixin) Fields() []ent.Field {
	return []ent.Field{
		field.Time("created_at").Immutable().Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

// Indexes of the TimeMixin.
func (TimeMixin) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("updated_at"),
	}
}
