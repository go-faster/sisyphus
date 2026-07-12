package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// InvestigationJob is a /investigate request run by ssagent's async job
// worker. Persisting it (instead of running the LLM tool-calling loop inline
// in the HTTP handler) lets a dropped client connection, an ssagent restart,
// or a client retry all be handled without losing or duplicating an
// in-flight investigation: the client polls GET /investigate/{id} instead of
// holding one long-lived request open, and IdempotencyKey lets a retried
// submission return the existing job instead of starting a second one.
type InvestigationJob struct {
	ent.Schema
}

func (InvestigationJob) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.String("idempotency_key").NotEmpty().Immutable(),
		field.Text("description").NotEmpty().Immutable(),
		field.String("status").Default("pending"),
		field.JSON("report", map[string]any{}).Optional(),
		field.Int("iterations").Default(0),
		field.Int("tools_used").Default(0),
		field.Text("error_message").Optional(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
		field.Time("started_at").Optional().Nillable(),
		field.Time("completed_at").Optional().Nillable(),
	}
}

func (InvestigationJob) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("idempotency_key").Unique(),
		index.Fields("status"),
	}
}
