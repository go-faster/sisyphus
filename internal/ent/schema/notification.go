package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Notification is one pending/delivered outbound message in the
// notification outbox: written by a collector (e.g. ssingest's notify
// dispatcher), drained and delivered by a sink's host process (e.g. ssbot for
// the Telegram channel), which then acks it via status. DedupKey is unique so
// a collector that re-emits the same event for the same user is a no-op,
// matching internal/agentstore.Store.Submit's idempotency-key pattern.
type Notification struct {
	ent.Schema
}

func (Notification) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.String("dedup_key").NotEmpty().Immutable(),
		field.UUID("user_id", uuid.UUID{}),
		field.String("channel").NotEmpty().Immutable(),
		field.Int64("telegram_user_id").Optional(),
		field.Int64("telegram_access_hash").Optional().Nillable(),
		field.String("source").NotEmpty().Immutable(),
		field.String("event_type").NotEmpty().Immutable(),
		field.Text("text").NotEmpty().Immutable(),
		field.String("url").Optional(),
		field.String("status").Default("pending"),
		field.Int("attempts").Default(0),
		field.Text("error").Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
		field.Time("delivered_at").Optional().Nillable(),
	}
}

func (Notification) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", NotifyUser.Type).
			Ref("notifications").
			Field("user_id").
			Unique().
			Required(),
	}
}

func (Notification) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("dedup_key").Unique(),
		index.Fields("status"),
	}
}
