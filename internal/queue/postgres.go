package queue

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"

	"github.com/go-faster/errors"
	"github.com/google/uuid"
)

// execQuerier is the subset of ent's generated client used here. Both
// *ent.Client and *ent.Tx satisfy it (the sql/execquery feature), which is
// what lets a Postgres queue publish inside a caller's transaction.
type execQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// PostgresOptions configures a [Postgres] queue.
type PostgresOptions struct {
	// MaxAttempts is the default attempt budget per message.
	MaxAttempts int
	// Lease is how long a claim is held before the job returns to the pool.
	// It must comfortably exceed the slowest expected job: a job still
	// running when its lease expires gets claimed a second time.
	Lease time.Duration
	// Backoff maps a failed attempt count to the delay before the next claim.
	Backoff func(attempt int) time.Duration
	// Owner identifies the claiming process in lease_owner, for debugging.
	Owner string
	// Now is the clock, injectable so tests need no real sleeps.
	Now func() time.Time
}

func (opts *PostgresOptions) setDefaults() {
	if opts.MaxAttempts == 0 {
		opts.MaxAttempts = 5
	}
	if opts.Lease == 0 {
		opts.Lease = 5 * time.Minute
	}
	if opts.Backoff == nil {
		opts.Backoff = ExponentialBackoff(30*time.Second, 10*time.Minute)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
}

// Postgres is a [Queue] backed by the queue_jobs table. Claims use
// FOR UPDATE SKIP LOCKED, so N workers can drain the same queue concurrently
// without coordination and without blocking each other.
type Postgres struct {
	db   execQuerier
	name string
	opts PostgresOptions
}

var _ Queue = (*Postgres)(nil)

// NewPostgres creates a queue over the logical stream name. Jobs of different
// names never interleave, so one table serves every queue in the system.
func NewPostgres(db execQuerier, name string, opts PostgresOptions) *Postgres {
	opts.setDefaults()
	return &Postgres{db: db, name: name, opts: opts}
}

// WithTx returns a copy that reads and writes through tx, so a caller can
// enqueue work in the same transaction as the domain row that work refers to.
// This is the transactional-outbox guarantee, and it is specific to this
// implementation — the [Queue] interface does not promise it.
func (p *Postgres) WithTx(tx execQuerier) *Postgres {
	c := *p
	c.db = tx
	return &c
}

const publishPrefix = `INSERT INTO queue_jobs
	(id, queue, dedup_key, payload, status, attempts, max_attempts, available_at, created_at, updated_at)
VALUES `

// Publish inserts msgs, ignoring any whose key already exists in this queue.
// Dedup covers the row's whole lifetime, not just while it is outstanding: a
// key that was published and completed is still refused. That is what callers
// want for an idempotency or event key, and it means keys that should recur
// (a periodic ingest run) must carry something that varies, such as a cursor
// or window bound.
func (p *Postgres) Publish(ctx context.Context, msgs ...Message) (int, error) {
	if len(msgs) == 0 {
		return 0, nil
	}

	now := p.opts.Now()
	var (
		values strings.Builder
		args   = make([]any, 0, len(msgs)*8)
	)
	for i, m := range msgs {
		if m.Key == "" {
			return 0, errors.Errorf("message %d: empty dedup key", i)
		}
		maxAttempts := m.MaxAttempts
		if maxAttempts == 0 {
			maxAttempts = p.opts.MaxAttempts
		}
		if i > 0 {
			values.WriteString(", ")
		}
		values.WriteString("(")
		for j := range 10 {
			if j > 0 {
				values.WriteString(", ")
			}
			values.WriteString("$")
			values.WriteString(strconv.Itoa(len(args) + j + 1))
		}
		values.WriteString(")")
		id := m.ID
		if id == uuid.Nil {
			id = uuid.New()
		}
		args = append(args,
			id, p.name, m.Key, m.Payload,
			StatusPending, 0, maxAttempts, now.Add(m.Delay), now, now,
		)
	}

	res, err := p.db.ExecContext(ctx,
		publishPrefix+values.String()+" ON CONFLICT (queue, dedup_key) DO NOTHING",
		args...,
	)
	if err != nil {
		return 0, errors.Wrap(err, "insert queue jobs")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, errors.Wrap(err, "rows affected")
	}
	return int(n), nil
}

// claimQuery locks candidate rows and marks them running in one statement.
// SKIP LOCKED is what makes concurrent workers safe: a row another worker is
// mid-claim on is passed over instead of blocking this one.
//
// The candidate predicate covers both a never-claimed job and one whose lease
// expired because its worker died, which is why no separate requeue step is
// needed to recover from a crash.
const claimQuery = `WITH ready AS (
	SELECT id FROM queue_jobs
	WHERE queue = $1
	  AND attempts < max_attempts
	  AND (
	        (status = 'pending' AND available_at <= $2)
	     OR (status = 'running' AND lease_expires_at <= $2)
	  )
	ORDER BY available_at, created_at
	FOR UPDATE SKIP LOCKED
	LIMIT $3
)
UPDATE queue_jobs j
SET status = 'running',
    attempts = j.attempts + 1,
    lease_expires_at = $4,
    lease_owner = $5,
    updated_at = $2
FROM ready
WHERE j.id = ready.id
RETURNING j.id, j.dedup_key, j.payload, j.attempts, j.max_attempts`

// Fetch claims up to limit ready jobs.
func (p *Postgres) Fetch(ctx context.Context, limit int) ([]Delivery, error) {
	if limit <= 0 {
		return nil, nil
	}
	now := p.opts.Now()
	rows, err := p.db.QueryContext(ctx, claimQuery,
		p.name, now, limit, now.Add(p.opts.Lease), p.opts.Owner,
	)
	if err != nil {
		return nil, errors.Wrap(err, "claim queue jobs")
	}
	defer func() { _ = rows.Close() }()

	var out []Delivery
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.ID, &d.Key, &d.Payload, &d.Attempts, &d.MaxAttempts); err != nil {
			return nil, errors.Wrap(err, "scan queue job")
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "iterate queue jobs")
	}
	return out, nil
}

const ackQuery = `UPDATE queue_jobs
SET status = $2, completed_at = $3, updated_at = $3, lease_expires_at = NULL, lease_owner = ''
WHERE id = $1`

// Ack marks a delivery done. It is unconditional and therefore idempotent:
// acking twice, or acking after the lease expired and another worker
// re-claimed the job, both settle on done. The queue is at-least-once, so a
// consumer must be idempotent regardless.
func (p *Postgres) Ack(ctx context.Context, id uuid.UUID) error {
	res, err := p.db.ExecContext(ctx, ackQuery, id, StatusDone, p.opts.Now())
	if err != nil {
		return errors.Wrap(err, "ack queue job")
	}
	return checkAffected(res)
}

const nackSelect = `SELECT attempts, max_attempts FROM queue_jobs WHERE id = $1`

const nackRetry = `UPDATE queue_jobs
SET status = $2, available_at = $3, error = $4, updated_at = $5, lease_expires_at = NULL, lease_owner = ''
WHERE id = $1`

const nackTerminal = `UPDATE queue_jobs
SET status = $2, error = $3, completed_at = $4, updated_at = $4, lease_expires_at = NULL, lease_owner = ''
WHERE id = $1`

// Nack returns a failed delivery to the queue, delayed by the configured
// backoff. Once the job has used its attempt budget it becomes terminal
// (StatusError) instead, so a permanently failing message stops consuming
// worker slots but stays visible to an operator.
func (p *Postgres) Nack(ctx context.Context, id uuid.UUID, cause error) error {
	var attempts, maxAttempts int
	rows, err := p.db.QueryContext(ctx, nackSelect, id)
	if err != nil {
		return errors.Wrap(err, "read queue job attempts")
	}
	found := rows.Next()
	if found {
		if err := rows.Scan(&attempts, &maxAttempts); err != nil {
			_ = rows.Close()
			return errors.Wrap(err, "scan queue job attempts")
		}
	}
	if err := rows.Close(); err != nil {
		return errors.Wrap(err, "close queue job attempts")
	}
	if !found {
		return ErrNotFound
	}

	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	now := p.opts.Now()

	var res sql.Result
	if attempts >= maxAttempts {
		res, err = p.db.ExecContext(ctx, nackTerminal, id, StatusError, msg, now)
	} else {
		res, err = p.db.ExecContext(ctx, nackRetry,
			id, StatusPending, now.Add(p.opts.Backoff(attempts)), msg, now,
		)
	}
	if err != nil {
		return errors.Wrap(err, "nack queue job")
	}
	return checkAffected(res)
}

const reapQuery = `UPDATE queue_jobs
SET status = $2, completed_at = $3, updated_at = $3, lease_expires_at = NULL, lease_owner = '',
    error = CASE WHEN COALESCE(error, '') = '' THEN $4 ELSE error END
WHERE queue = $1
  AND status IN ('pending', 'running')
  AND attempts >= max_attempts
  AND (status = 'pending' OR lease_expires_at <= $3)`

// ReapStale settles jobs that can never be claimed again: their attempts are
// spent and no worker holds a live lease. Without it those rows sit in a
// non-terminal status forever — invisible to Fetch, but indistinguishable
// from real backlog in any "how much work is outstanding" query.
//
// It is not required for correctness of retries; an expired lease alone is
// enough for [Postgres.Fetch] to reclaim a crashed worker's job.
func (p *Postgres) ReapStale(ctx context.Context) (int, error) {
	res, err := p.db.ExecContext(ctx, reapQuery,
		p.name, StatusError, p.opts.Now(), "attempts exhausted without acknowledgement",
	)
	if err != nil {
		return 0, errors.Wrap(err, "reap stale queue jobs")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, errors.Wrap(err, "rows affected")
	}
	return int(n), nil
}

func checkAffected(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return errors.Wrap(err, "rows affected")
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
