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
	// A [Worker] gives its handler exactly this long, so the handler cannot
	// outlive the claim.
	Lease time.Duration
	// Backoff maps a failed attempt count to the delay before the next claim.
	Backoff func(attempt int) time.Duration
	// Owner identifies the claiming process in lease_owner, for debugging.
	Owner string
	// Now overrides the clock, for tests only.
	//
	// Leave it nil in production: the queue then reads time from Postgres, so
	// every worker compares visibility against one clock. Deriving it from
	// each process's own clock instead makes a replica whose clock runs fast
	// see live claims as expired and steal them.
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

// clock is the value passed wherever a query says COALESCE($n, now()): nil in
// production, so Postgres supplies the time, and a fixed instant under test.
func (p *Postgres) clock() any {
	if p.opts.Now == nil {
		return nil
	}
	return p.opts.Now()
}

const publishPrefix = `INSERT INTO queue_jobs
	(id, queue, dedup_key, payload, status, attempts, max_attempts, visible_at, created_at, updated_at)
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

	var (
		values strings.Builder
		args   = make([]any, 0, len(msgs)*9+1)
	)
	// $1 is the shared clock for every row, so one batch cannot straddle two
	// instants.
	args = append(args, p.clock())
	for i, m := range msgs {
		if m.Key == "" {
			return 0, errors.Errorf("message %d: empty dedup key", i)
		}
		maxAttempts := m.MaxAttempts
		if maxAttempts == 0 {
			maxAttempts = p.opts.MaxAttempts
		}
		id := m.ID
		if id == uuid.Nil {
			id = uuid.New()
		}

		if i > 0 {
			values.WriteString(", ")
		}
		base := len(args)
		values.WriteString("(")
		for j := range 7 {
			if j > 0 {
				values.WriteString(", ")
			}
			values.WriteString("$")
			values.WriteString(strconv.Itoa(base + j + 1))
		}
		// visible_at, created_at, updated_at all derive from the shared clock.
		values.WriteString(", COALESCE($1, now()) + make_interval(secs => $")
		values.WriteString(strconv.Itoa(base + 8))
		values.WriteString("), COALESCE($1, now()), COALESCE($1, now()))")

		args = append(args,
			id, p.name, m.Key, m.Payload,
			StatusPending, 0, maxAttempts, m.Delay.Seconds(),
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
// The predicate is a single range over visible_at, which is why the partial
// index on (queue, visible_at) can serve both the filter and the ORDER BY.
// It covers a never-claimed job and one whose claim lapsed because its worker
// died with the same comparison — no separate requeue step recovers a crash,
// the visibility clock does.
const claimQuery = `WITH ready AS (
	SELECT id FROM queue_jobs
	WHERE queue = $1
	  AND status IN ('pending', 'running')
	  AND visible_at <= COALESCE($2, now())
	  AND attempts < max_attempts
	ORDER BY visible_at
	FOR UPDATE SKIP LOCKED
	LIMIT $3
)
UPDATE queue_jobs j
SET status = 'running',
    attempts = j.attempts + 1,
    visible_at = COALESCE($2, now()) + make_interval(secs => $4),
    lease_owner = $5,
    updated_at = COALESCE($2, now())
FROM ready
WHERE j.id = ready.id
RETURNING j.id, j.dedup_key, j.payload, j.attempts, j.max_attempts, j.visible_at`

// Fetch claims up to limit ready jobs.
func (p *Postgres) Fetch(ctx context.Context, limit int) ([]Delivery, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := p.db.QueryContext(ctx, claimQuery,
		p.name, p.clock(), limit, p.opts.Lease.Seconds(), p.opts.Owner,
	)
	if err != nil {
		return nil, errors.Wrap(err, "claim queue jobs")
	}
	defer func() { _ = rows.Close() }()

	var out []Delivery
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.ID, &d.Key, &d.Payload, &d.Attempts, &d.MaxAttempts, &d.Deadline); err != nil {
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
SET status = $2, completed_at = COALESCE($3, now()), updated_at = COALESCE($3, now()), lease_owner = ''
WHERE id = $1`

// Ack marks a delivery done. It is unconditional and therefore idempotent:
// acking twice, or acking after the claim lapsed and another worker took the
// job, both settle on done. The queue is at-least-once, so a consumer must be
// idempotent regardless.
func (p *Postgres) Ack(ctx context.Context, id uuid.UUID) error {
	res, err := p.db.ExecContext(ctx, ackQuery, id, StatusDone, p.clock())
	if err != nil {
		return errors.Wrap(err, "ack queue job")
	}
	return checkAffected(res)
}

const nackSelect = `SELECT attempts, max_attempts FROM queue_jobs WHERE id = $1`

const nackRetry = `UPDATE queue_jobs
SET status = $2, visible_at = COALESCE($3, now()) + make_interval(secs => $4), error = $5,
    updated_at = COALESCE($3, now()), lease_owner = ''
WHERE id = $1`

const nackTerminal = `UPDATE queue_jobs
SET status = $2, error = $3, completed_at = COALESCE($4, now()), updated_at = COALESCE($4, now()),
    lease_owner = ''
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

	var res sql.Result
	if attempts >= maxAttempts {
		res, err = p.db.ExecContext(ctx, nackTerminal, id, StatusError, msg, p.clock())
	} else {
		res, err = p.db.ExecContext(ctx, nackRetry,
			id, StatusPending, p.clock(), p.opts.Backoff(attempts).Seconds(), msg,
		)
	}
	if err != nil {
		return errors.Wrap(err, "nack queue job")
	}
	return checkAffected(res)
}

const reapQuery = `UPDATE queue_jobs
SET status = $2, completed_at = COALESCE($3, now()), updated_at = COALESCE($3, now()), lease_owner = '',
    error = CASE WHEN COALESCE(error, '') = '' THEN $4 ELSE error END
WHERE queue = $1
  AND status IN ('pending', 'running')
  AND attempts >= max_attempts
  AND visible_at <= COALESCE($3, now())`

// ReapStale settles jobs that can never be claimed again: their attempts are
// spent and no worker holds a live claim. Without it those rows sit in a
// non-terminal status forever — invisible to Fetch, but indistinguishable
// from real backlog in any "how much work is outstanding" query.
//
// It is not required for correctness of retries; visibility lapsing alone is
// enough for [Postgres.Fetch] to reclaim a crashed worker's job.
func (p *Postgres) ReapStale(ctx context.Context) (int, error) {
	res, err := p.db.ExecContext(ctx, reapQuery,
		p.name, StatusError, p.clock(), "attempts exhausted without acknowledgement",
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
