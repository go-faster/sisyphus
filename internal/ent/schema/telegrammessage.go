package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// TelegramMessage is a raw Telegram message (plan §4).
type TelegramMessage struct {
	ent.Schema
}

func (TelegramMessage) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.Int64("chat_id"),
		field.Int64("message_id"),
		field.Int64("thread_id").Optional().Nillable(),
		field.Int64("sender_id").Optional().Nillable(),
		field.String("sender_name").Optional(),
		field.Text("text").Optional(),
		field.Time("message_date"),
		field.Int64("reply_to_id").Optional().Nillable(),
		field.JSON("raw_json", map[string]any{}).Default(map[string]any{}).
			Annotations(entsql.Default("'{}'")),
		field.Time("created_at").Default(time.Now),
	}
}

func (TelegramMessage) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("chat_id", "message_id").Unique(),
		index.Fields("chat_id", "message_date"),
	}
}
