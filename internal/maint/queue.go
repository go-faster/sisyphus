package maint

import (
	"context"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/notify/store"
	"github.com/go-faster/sisyphus/internal/queue"
)

// Queue job names.
const (
	JobReapStale       = "reap-stale"
	JobQueueRetention  = "queue-retention"
	JobNotifyRetention = "notify-retention"
)

// ReapStale settles jobs across every queue that can never be claimed again:
// attempts spent, no live lease.
//
// It runs over all queues from one place because the condition arises at
// runtime, not at startup, and because it was previously wired per-owner —
// which is how notify delivery ended up with no reaper at all. A job another
// process is still running is left alone.
func ReapStale(ctx context.Context, queues []*queue.Postgres) error {
	lg := zctx.From(ctx)

	var failed error
	for _, q := range queues {
		n, err := q.ReapStale(ctx)
		if err != nil {
			// One unreachable queue must not skip the others: they are
			// independent, and the next tick retries this one anyway.
			failed = errors.Join(failed, errors.Wrapf(err, "reap %s", q.Name()))
			continue
		}
		if n > 0 {
			lg.Warn("reaped jobs abandoned without acknowledgement",
				zap.String("queue", q.Name()), zap.Int("count", n))
		}
	}
	return failed
}

// PurgeQueues deletes settled jobs past their retention window in every queue.
func PurgeQueues(ctx context.Context, queues []*queue.Postgres, opts queue.PurgeOptions) error {
	lg := zctx.From(ctx)

	var failed error
	for _, q := range queues {
		rep, err := q.Purge(ctx, opts)
		if rep.Total() > 0 || rep.Capped {
			lg.Info("purged settled queue jobs",
				zap.String("queue", q.Name()),
				zap.Int("done", rep.Done),
				zap.Int("error", rep.Error),
				// Capped every run means retention is not keeping up with the
				// queue's churn, which wants a larger batch, not a shrug.
				zap.Bool("capped", rep.Capped),
			)
		}
		if err != nil {
			failed = errors.Join(failed, errors.Wrapf(err, "purge %s", q.Name()))
		}
	}
	return failed
}

// PurgeNotifications deletes settled notification rows past their window.
func PurgeNotifications(ctx context.Context, s *store.Store, opts store.PurgeOptions) error {
	rep, err := s.Purge(ctx, opts)
	if rep.Deleted > 0 || rep.Capped {
		zctx.From(ctx).Info("purged settled notifications",
			zap.Int("deleted", rep.Deleted),
			zap.Bool("capped", rep.Capped),
		)
	}
	return err
}
