// Package schema defines ent schema types for database tables.
package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Chunk is a retrievable unit derived from a Document (plan §1).
type Chunk struct {
	ent.Schema
}

func (Chunk) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.UUID("document_id", uuid.UUID{}),
		field.Int("chunk_index"),
		field.String("chunk_type").NotEmpty(),
		field.String("title").Optional(),
		field.Text("text").NotEmpty(),
		field.String("text_hash").NotEmpty(),
		field.JSON("metadata", map[string]any{}).Default(map[string]any{}).
			Annotations(entsql.Default("'{}'")),
		field.Int("token_count").Optional(),
		field.UUID("qdrant_point_id", uuid.UUID{}).Optional().Nillable(),
	}
}

func (Chunk) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("document", Document.Type).
			Ref("chunks").
			Field("document_id").
			Unique().
			Required(),
	}
}

func (Chunk) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("document_id", "chunk_index", "text_hash").Unique(),
		index.Fields("metadata").Annotations(entsql.IndexType("GIN")),
	}
}
