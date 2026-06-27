package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// SupportRequest is a detected Telegram support conversation (plan §4).
type SupportRequest struct {
	ent.Schema
}

func (SupportRequest) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.Int64("chat_id"),
		field.Int64("first_message_id"),
		field.Int64("last_message_id").Optional().Nillable(),
		field.String("source_url").Optional(),
		field.Text("raw_text").NotEmpty(),
		field.Text("summary").Optional(),
		field.String("service_guess").Optional(),
		field.String("severity_guess").Optional(),
		field.String("status").Default("new"),
		field.Float("confidence").Optional().Nillable(),
		field.JSON("metadata", map[string]any{}).Default(map[string]any{}).
			Annotations(entsql.Default("'{}'")),
		field.Time("created_at").Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (SupportRequest) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("chat_id", "first_message_id").Unique(),
		index.Fields("status"),
	}
}
