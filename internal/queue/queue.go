// Package queue is the shared substrate for background work: ingest runs,
// notification delivery, and agent investigations all enqueue here instead of
// each growing its own bespoke "pending rows + status column" loop.
//
// The interface deliberately says nothing about Postgres. It carries payloads
// rather than row IDs, and acknowledges by delivery ID rather than by handing
// out a live transaction, so a broker-backed implementation is possible
// without changing a single producer or consumer. Two things a queue
// therefore cannot promise, and which callers must not assume:
//
//   - Dedup is best-effort. [Queue.Publish] reports how many messages it
//     actually enqueued, but a caller must still be idempotent on redelivery.
//   - Enqueue is not transactional with the caller's own writes unless the
//     implementation says so. The Postgres implementation does support it (see
//     [Postgres.WithTx]); that is a property of that implementation, not of
//     this interface.
//
// Job state of record — an investigation's report, a notification's delivery
// outcome — stays on its own domain row, not here. A queue answers "what work
// is outstanding", never "what happened to job X".
package queue

import (
	"context"
	"time"

	"github.com/go-faster/errors"
	"github.com/google/uuid"
)

// Job status values.
const (
	// StatusPending is a job waiting to be claimed, either never claimed or
	// returned to the pool by a failed attempt.
	StatusPending = "pending"
	// StatusRunning is a job claimed by a worker and covered by a lease.
	StatusRunning = "running"
	// StatusDone is an acknowledged job.
	StatusDone = "done"
	// StatusError is a job that exhausted its attempts. Terminal: it is never
	// claimed again and stays for operator inspection.
	StatusError = "error"
)

// ErrNotFound is returned when acknowledging a delivery that no longer exists.
var ErrNotFound = errors.New("queue job not found")

// Message is work to enqueue.
type Message struct {
	// ID, if set, becomes the delivery's ID. Producers that keep a domain row
	// per job set it to that row's ID, so acknowledging a delivery and
	// updating its domain row address the same identifier and no lookup is
	// needed to get from one to the other. Zero means the queue assigns one.
	ID uuid.UUID
	// Key deduplicates: publishing a Key already outstanding in this queue is
	// a no-op. Required.
	Key string
	// Payload is opaque to the queue; only the producer and its matching
	// consumer interpret it.
	Payload []byte
	// Delay holds the message back from being claimed for this long. Zero
	// makes it immediately available.
	Delay time.Duration
	// MaxAttempts overrides the queue's default attempt budget for this
	// message. Zero uses the default.
	MaxAttempts int
}

// Delivery is a claimed message. The claim is covered by a lease: a worker
// that neither [Queue.Ack]s nor [Queue.Nack]s before the lease expires loses
// it, and the job becomes claimable again — which is what makes a worker
// crash recoverable rather than a permanently stuck row.
type Delivery struct {
	ID      uuid.UUID
	Key     string
	Payload []byte
	// Attempts counts claims including this one, so it is always >= 1. A
	// consumer can use it to log or to degrade behavior on a retry.
	Attempts int
	// MaxAttempts is this delivery's attempt budget. Attempts == MaxAttempts
	// means a Nack sends the job to StatusError instead of retrying.
	MaxAttempts int
}

// LastAttempt reports whether a Nack of this delivery is terminal.
func (d Delivery) LastAttempt() bool { return d.Attempts >= d.MaxAttempts }

// Queue is the substrate contract. Implementations must be safe for
// concurrent use, and Fetch must never hand the same message to two callers
// while a lease is live.
type Queue interface {
	// Publish enqueues msgs, skipping any whose Key is already outstanding.
	// It returns how many were actually enqueued.
	Publish(ctx context.Context, msgs ...Message) (int, error)
	// Fetch claims up to limit ready messages, oldest first, and leases them
	// to the caller. It returns an empty slice — not an error — when there is
	// no work.
	Fetch(ctx context.Context, limit int) ([]Delivery, error)
	// Ack marks a delivery permanently done.
	Ack(ctx context.Context, id uuid.UUID) error
	// Nack reports a failed attempt. The message is retried after a backoff
	// unless its attempts are exhausted, in which case it becomes terminal.
	Nack(ctx context.Context, id uuid.UUID, cause error) error
}
