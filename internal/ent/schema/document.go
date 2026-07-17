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

// Document is a normalized source artifact (plan §1).
type Document struct {
	ent.Schema
}

func (Document) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.String("source").NotEmpty(),
		field.String("source_id").NotEmpty(),
		field.String("source_url").Optional(),
		field.String("title").Optional(),
		field.Text("body").Optional(),
		field.String("body_hash").NotEmpty(),
		field.JSON("metadata", map[string]any{}).Default(map[string]any{}).
			Annotations(entsql.Default("{}")),
		// chunker_version records which version of the chunker produced this
		// document's chunks. The body hash cannot detect a chunker change — the
		// body is identical, the code that splits it is not — so without this a
		// chunking change can never reach documents already indexed. 0 means the
		// chunker reports no version, which is the pre-existing behavior.
		field.Int("chunker_version").Default(0),
		field.Time("created_at").Optional(),
		field.Time("updated_at").Optional(),
		field.Time("captured_at").Default(time.Now),
	}
}

func (Document) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("chunks", Chunk.Type),
	}
}

func (Document) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("source", "source_id").Unique(),
		index.Fields("metadata").Annotations(entsql.IndexType("GIN")),
	}
}
