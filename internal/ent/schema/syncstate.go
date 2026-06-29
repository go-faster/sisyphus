package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// SyncState is the schema for per-source ingestion cursor state.
type SyncState struct {
	ent.Schema
}

func (SyncState) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.String("source").NotEmpty(),
		field.Time("last_synced_at").Optional().Nillable(),
		field.String("last_cursor").Default(""),
		field.String("status").Default("new"),
		field.Text("error").Optional().Nillable(),
		field.Int("document_count").Default(0),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (SyncState) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("source").Unique(),
		index.Fields("status"),
	}
}
