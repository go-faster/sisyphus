package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// QueueJob is one unit of background work: the delivery record for a job a
// worker must run exactly once. It is deliberately domain-agnostic — Payload
// is an opaque blob only the queue's producer and consumer understand — so
// one table backs every queue (ingest, notify delivery, agent investigations)
// with one set of indexes and one claim implementation.
//
// A QueueJob owns delivery state only: attempts, visibility, terminal
// outcome. Business state (an investigation's report, a notification's
// delivered_at) stays on its own domain row, which is what a client polls by
// ID. Keeping the two apart is what lets internal/queue's interface stay
// implementable on something other than Postgres.
type QueueJob struct {
	ent.Schema
}

func (QueueJob) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		// Queue names the logical stream ("notify.telegram", "agent.investigate").
		// Workers claim within one queue; queues never interleave.
		field.String("queue").NotEmpty().Immutable(),
		// DedupKey makes Publish idempotent: re-publishing a key already
		// present in this queue is a no-op, in any state. Unique per queue,
		// so two queues may carry the same business key.
		field.String("dedup_key").NotEmpty().Immutable(),
		field.Bytes("payload").Optional(),
		field.String("status").Default("pending"),
		field.Int("attempts").Default(0),
		field.Int("max_attempts").Default(5),
		// VisibleAt is the single visibility clock, serving both waiting and
		// running jobs: for a pending job it is when the job may be claimed
		// (set ahead to delay a publish or back off a retry), and for a
		// running one it is when its claim lapses and it becomes claimable
		// again.
		//
		// One column rather than a separate available_at/lease_expires_at
		// pair, so the claim predicate is a single range scan that an index
		// can also order by. Split across two columns, the OR between them
		// forces Postgres to sort every matching row before LIMIT — which on
		// a backlog means an external merge sort on every claim.
		field.Time("visible_at").Default(time.Now),
		field.String("lease_owner").Optional(),
		field.Text("error").Optional(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
		field.Time("completed_at").Optional().Nillable(),
	}
}

func (QueueJob) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("queue", "dedup_key").Unique(),
		// The claim and reap access path. Partial, so the index holds only
		// outstanding work: completed and dead-lettered jobs drop out of it
		// entirely and a growing history costs claims nothing.
		index.Fields("queue", "visible_at").
			Annotations(entsql.IndexWhere("status IN ('pending', 'running')")),
	}
}
