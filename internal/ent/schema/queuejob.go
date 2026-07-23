package schema

import (
	"time"

	"entgo.io/ent"
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
// A QueueJob owns delivery state only: attempts, lease, terminal outcome.
// Business state (an investigation's report, a notification's delivered_at)
// stays on its own domain row, which is what a client polls by ID. Keeping
// the two apart is what lets internal/queue's interface stay implementable on
// something other than Postgres.
type QueueJob struct {
	ent.Schema
}

func (QueueJob) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		// Queue names the logical stream ("notify.telegram", "agent.investigate").
		// Workers claim within one queue; queues never interleave.
		field.String("queue").NotEmpty().Immutable(),
		// DedupKey makes Publish idempotent: re-publishing a key that is
		// already queued is a no-op. Unique per queue, so two queues may
		// legitimately carry the same business key.
		field.String("dedup_key").NotEmpty().Immutable(),
		field.Bytes("payload").Optional(),
		field.String("status").Default("pending"),
		field.Int("attempts").Default(0),
		field.Int("max_attempts").Default(5),
		// AvailableAt gates when a job may be claimed: set ahead on Nack to
		// implement retry backoff, and to now() on publish.
		field.Time("available_at").Default(time.Now),
		// LeaseExpiresAt is when a claimed job returns to the pool. A worker
		// that crashes mid-job never acks, so the lease expiring is the only
		// thing that makes the job runnable again.
		field.Time("lease_expires_at").Optional().Nillable(),
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
		// The claim query's access path: ready jobs of one queue, oldest first.
		index.Fields("queue", "status", "available_at"),
		// Lease reaping scans claimed jobs across every queue.
		index.Fields("status", "lease_expires_at"),
	}
}
