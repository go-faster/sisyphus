package store

import (
	"context"
	"time"

	"github.com/go-faster/errors"

	"github.com/go-faster/sisyphus/internal/ent/notification"
)

// PurgeOptions bounds one notification retention sweep.
type PurgeOptions struct {
	// After is how long a settled notification is kept, measured from when it
	// last changed. 0 keeps them forever.
	//
	// Both delivered and errored rows share one window, unlike the queue's two:
	// a Notification is the record of what was said, and a failed one is no
	// more interesting to keep than a successful one once someone has looked.
	After time.Duration
	// Batch is how many rows one DELETE removes.
	Batch int
	// MaxBatches caps one sweep. Whatever is left goes in the next one.
	MaxBatches int
	// Now overrides the clock, for tests only.
	//
	// Unlike the queue's visibility clock, this one is read from Go rather than
	// from Postgres. That rule exists because two replicas comparing a lease
	// against their own clocks steal each other's live claims; a retention
	// window is days wide, read by one process at a time under the maintenance
	// lock, and seconds of skew cannot change which rows it selects.
	Now func() time.Time
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
	// Deleted is how many notification rows were removed.
	Deleted int
	// Capped reports that MaxBatches was reached with rows still eligible.
	Capped bool
}

// Purge deletes settled notifications older than opts.After.
//
// Only delivered and errored rows are eligible: a pending row is outstanding
// work, however old, and deleting one would strand the queue job that delivers
// it. Rows are matched on updated_at, which is when the row settled — a
// delivered row has delivered_at too, but an errored one does not, and the two
// want the same window.
//
// This can run in any order relative to the queue's own retention. Both sides
// key their queue jobs by the notification's row ID (see outbox.go), so a row
// removed here can never collide with a job that outlived it: the next
// notification for the same event gets a new ID and therefore a new key.
func (s *Store) Purge(ctx context.Context, opts PurgeOptions) (PurgeReport, error) {
	opts.setDefaults()

	var rep PurgeReport
	if opts.After <= 0 {
		return rep, nil
	}

	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	cutoff := now().Add(-opts.After)

	for range opts.MaxBatches {
		ids, err := s.db.Notification.Query().
			Where(
				notification.StatusIn(StatusDelivered, StatusError),
				notification.UpdatedAtLT(cutoff),
			).
			Limit(opts.Batch).
			IDs(ctx)
		if err != nil {
			return rep, errors.Wrap(err, "select settled notifications")
		}
		if len(ids) == 0 {
			return rep, nil
		}

		n, err := s.db.Notification.Delete().
			Where(notification.IDIn(ids...)).
			Exec(ctx)
		if err != nil {
			return rep, errors.Wrap(err, "delete notifications")
		}
		rep.Deleted += n

		if len(ids) < opts.Batch {
			return rep, nil
		}
	}
	rep.Capped = true
	return rep, nil
}
