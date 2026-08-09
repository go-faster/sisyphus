package queue

import (
	"context"
	"time"

	"github.com/go-faster/errors"
)

// PurgeOptions bounds one retention sweep.
//
// Terminal rows are kept for different reasons and so for different windows: a
// done row is an audit trail nobody reads, while an error row is the record of
// something that needs looking at. Both are separately disableable, because
// "keep failures forever" is a legitimate policy and "keep everything forever"
// is what the table does today.
type PurgeOptions struct {
	// DoneAfter is how long an acknowledged job is kept. 0 keeps them forever.
	DoneAfter time.Duration
	// ErrorAfter is how long a job that exhausted its attempts is kept. 0 keeps
	// them forever.
	ErrorAfter time.Duration
	// Batch is how many rows one DELETE removes.
	//
	// Deleting in batches is not an optimization: an unbounded DELETE over a
	// churn table takes a long-lived lock and bloats WAL, which is the failure
	// dataddo/pgq warns about explicitly.
	Batch int
	// MaxBatches caps one sweep, so a first run against a long-neglected table
	// cannot delete for hours while holding the maintenance lock. Whatever is
	// left is deleted by the next sweep.
	MaxBatches int
}

const (
	defaultPurgeBatch      = 5000
	defaultPurgeMaxBatches = 20
)

func (opts *PurgeOptions) setDefaults() {
	if opts.Batch <= 0 {
		opts.Batch = defaultPurgeBatch
	}
	if opts.MaxBatches <= 0 {
		opts.MaxBatches = defaultPurgeMaxBatches
	}
}

// PurgeReport summarizes one sweep.
type PurgeReport struct {
	// Done and Error count the rows deleted in each status.
	Done  int
	Error int
	// Capped reports that MaxBatches was reached with rows still eligible, so
	// the sweep stopped early. Persistent capping means retention is not
	// keeping up with the queue's churn.
	Capped bool
}

// Total is how many rows the sweep deleted.
func (r PurgeReport) Total() int { return r.Done + r.Error }

// purgeQuery deletes one batch of settled jobs.
//
// It filters by ctid rather than id so the subquery can stop at LIMIT without
// the planner needing an index: terminal rows are deliberately outside the
// partial claim index (see the package CLAUDE.md), which is what keeps history
// free for claims, and adding an index just for retention would give that back.
const purgeQuery = `DELETE FROM queue_jobs
WHERE ctid IN (
    SELECT ctid FROM queue_jobs
    WHERE queue = $1
      AND status = $2
      AND completed_at IS NOT NULL
      AND completed_at < COALESCE($3, now()) - make_interval(secs => $4)
    LIMIT $5
)`

// Purge deletes settled jobs older than their status's retention window.
//
// Only terminal rows are eligible, and only by completed_at, so a job that is
// pending, running, or waiting out a backoff is never removed however old it
// is. It is safe to run against a live queue: claims never touch these rows.
//
// Deleting terminal rows is safe for dedup only because every producer
// publishes under a fresh or domain-row key (see the package CLAUDE.md). A
// producer that relied on the queue's lifetime dedup as its own idempotency
// record would be broken by retention, silently.
func (p *Postgres) Purge(ctx context.Context, opts PurgeOptions) (PurgeReport, error) {
	opts.setDefaults()

	var rep PurgeReport
	for _, w := range []struct {
		status  string
		after   time.Duration
		deleted *int
	}{
		{StatusDone, opts.DoneAfter, &rep.Done},
		{StatusError, opts.ErrorAfter, &rep.Error},
	} {
		if w.after <= 0 {
			continue
		}
		n, capped, err := p.purgeStatus(ctx, w.status, w.after, opts)
		*w.deleted = n
		rep.Capped = rep.Capped || capped
		if err != nil {
			// Report what the sweep did reach: a partial purge is still
			// progress, and the next run re-finds the rest.
			return rep, errors.Wrapf(err, "purge %s", w.status)
		}
	}
	return rep, nil
}

func (p *Postgres) purgeStatus(ctx context.Context, status string, after time.Duration, opts PurgeOptions) (deleted int, capped bool, _ error) {
	for range opts.MaxBatches {
		res, err := p.db.ExecContext(ctx, purgeQuery,
			p.name, status, p.clock(), after.Seconds(), opts.Batch,
		)
		if err != nil {
			return deleted, false, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return deleted, false, errors.Wrap(err, "rows affected")
		}
		deleted += int(n)
		if int(n) < opts.Batch {
			// A short batch means the window is drained.
			return deleted, false, nil
		}
	}
	return deleted, true, nil
}
