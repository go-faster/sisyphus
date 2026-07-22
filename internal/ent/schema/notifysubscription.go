package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// NotifySubscription is one NotifyUser's opt-in to a source's event types,
// optionally narrowed by filters (e.g. specific projects).
type NotifySubscription struct {
	ent.Schema
}

func (NotifySubscription) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.UUID("user_id", uuid.UUID{}),
		field.String("source").NotEmpty(),
		field.JSON("event_types", []string{}).Default([]string{}),
		field.JSON("filters", map[string]any{}).Default(map[string]any{}).
			Annotations(entsql.Default("{}")),
		field.Bool("enabled").Default(true),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (NotifySubscription) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", NotifyUser.Type).
			Ref("subscriptions").
			Field("user_id").
			Unique().
			Required(),
	}
}

func (NotifySubscription) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "source").Unique(),
		index.Fields("filters").Annotations(entsql.IndexType("GIN")),
	}
}
