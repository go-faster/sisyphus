package agentstore

import (
	"context"
	"encoding/json"
	"time"

	"github.com/go-faster/errors"
	"github.com/google/uuid"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/ent/investigationjob"
	"github.com/go-faster/sisyphus/internal/queue"
)

// QueueName is the queue investigations are dispatched through.
const QueueName = "agent.investigate"

// Payload is what a worker needs to run an investigation, carried by the
// queue rather than read back off the job row.
type Payload struct {
	Description string `json:"description"`
}

// Submit creates a job for idempotencyKey and queues it for a worker, or
// returns the already existing job if idempotencyKey was submitted before.
// created reports whether this call created the job.
//
// Unlike the previous in-process dispatch, a submitted job is durable work:
// any ssagent replica may pick it up, and one that dies mid-investigation has
// its job reclaimed when the lease lapses rather than leaving a client
// polling a row nobody is working on.
func (s *Store) Submit(ctx context.Context, idempotencyKey, description string) (job Job, created bool, err error) {
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return Job{}, false, errors.Wrap(err, "begin tx")
	}
	defer func() { _ = tx.Rollback() }()

	// The job row and its queue delivery share an ID, so a worker never has
	// to translate between the two.
	id := uuid.New()
	m, err := tx.InvestigationJob.Create().
		SetID(id).
		SetIdempotencyKey(idempotencyKey).
		SetDescription(description).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			existing, getErr := s.db.InvestigationJob.Query().
				Where(investigationjob.IdempotencyKey(idempotencyKey)).
				Only(ctx)
			if getErr != nil {
				return Job{}, false, errors.Wrap(getErr, "get existing job")
			}
			job, err = toJob(existing)
			return job, false, err
		}
		return Job{}, false, errors.Wrap(err, "create job")
	}

	body, err := json.Marshal(Payload{Description: description})
	if err != nil {
		return Job{}, false, errors.Wrap(err, "encode payload")
	}
	// Keyed by row ID, not by idempotency key: dedup is already enforced by
	// the row's unique index above, and a queue job outlives the row it
	// refers to, so sharing the key would swallow a later resubmission.
	if _, err := s.queue().WithTx(tx).Publish(ctx, queue.Message{
		ID:          id,
		Key:         id.String(),
		Payload:     body,
		MaxAttempts: s.opts.MaxAttempts,
	}); err != nil {
		return Job{}, false, errors.Wrap(err, "publish job")
	}

	if err := tx.Commit(); err != nil {
		return Job{}, false, errors.Wrap(err, "commit")
	}

	job, err = toJob(m)
	return job, true, err
}

// Queue returns the investigation queue, for a worker to drain.
func (s *Store) Queue() *queue.Postgres { return s.queue() }

func (s *Store) queue() *queue.Postgres {
	return queue.NewPostgres(s.db, QueueName, queue.PostgresOptions{
		MaxAttempts: s.opts.MaxAttempts,
		Lease:       s.opts.Lease,
		Owner:       s.opts.Owner,
		Now:         s.opts.Now,
	})
}

// reapQuery settles job rows whose delivery the queue has given up on.
const reapQuery = `UPDATE investigation_jobs j
SET status = $1, error_message = $2, completed_at = $3, updated_at = $3
FROM queue_jobs q
WHERE q.id = j.id
  AND q.queue = $4
  AND q.status = 'error'
  AND j.status IN ('pending', 'running')`

// ReapStale settles jobs the queue has abandoned: attempts spent and no live
// lease. Call it at startup and periodically.
//
// It deliberately does NOT fail every pending or running row. With work
// dispatched through a shared queue, a running row usually belongs to another
// replica that is still working on it, and failing it here would report a
// live investigation as dead.
func (s *Store) ReapStale(ctx context.Context) (int, error) {
	if _, err := s.queue().ReapStale(ctx); err != nil {
		return 0, errors.Wrap(err, "reap queue")
	}
	res, err := s.db.ExecContext(ctx, reapQuery,
		string(StatusError), "investigation abandoned after repeated failures", s.opts.Now(), QueueName,
	)
	if err != nil {
		return 0, errors.Wrap(err, "reap stale jobs")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, errors.Wrap(err, "rows affected")
	}
	return int(n), nil
}

// Options configures a [Store].
type Options struct {
	// Lease is how long a claimed investigation is held before another worker
	// may take it. It must exceed the job timeout, or a still-running
	// investigation can be started a second time elsewhere.
	Lease time.Duration
	// MaxAttempts is how many times an investigation may be claimed. The
	// default of 2 means one run plus one recovery if the worker dies: a
	// handler that records its own failure acknowledges the job, so retries
	// only ever cover a crash, never a failed investigation.
	MaxAttempts int
	// Owner identifies this process in claimed rows, for debugging.
	Owner string
	// Now is the clock, injectable for tests.
	Now func() time.Time
}

func (opts *Options) setDefaults() {
	if opts.Lease == 0 {
		opts.Lease = 10 * time.Minute
	}
	if opts.MaxAttempts == 0 {
		opts.MaxAttempts = 2
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
}
