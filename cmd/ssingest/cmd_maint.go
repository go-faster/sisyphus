package main

import (
	"context"
	"net/http"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"github.com/go-faster/sisyphus/internal/agentstore"
	"github.com/go-faster/sisyphus/internal/httpmw"
	"github.com/go-faster/sisyphus/internal/indexjob"
	"github.com/go-faster/sisyphus/internal/maint"
	"github.com/go-faster/sisyphus/internal/mcpserver"
	"github.com/go-faster/sisyphus/internal/notify"
	notifystore "github.com/go-faster/sisyphus/internal/notify/store"
	"github.com/go-faster/sisyphus/internal/queue"
	"github.com/go-faster/sisyphus/internal/reconcile"
	"github.com/go-faster/sisyphus/internal/search/qdrant"
	"github.com/go-faster/sisyphus/internal/vectorgc"
	"github.com/go-faster/sisyphus/internal/vectorrepair"
	"github.com/go-faster/sisyphus/internal/wire"
)

func newMaintCmd(deps *ingestDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "maint",
		Short: "run as a daemon: periodic vector gc and repair",
		Long: "Run maintenance on a schedule.\n\n" +
			"Every job here is also a one-shot subcommand (`ssingest gc`, `ssingest repair`),\n" +
			"and both paths take the same advisory lock, so a scheduled sweep and a hand-run\n" +
			"one never overlap. Intervals live under `maintenance.*`; setting one to 0\n" +
			"disables that job.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runMaint(cmd.Context(), deps)
		},
	}
}

// runMaint is the compose-side stand-in for the maintenance CronJobs
// ARCHITECTURE.md describes: a process whose only job is a timer.
//
// It is deliberately separate from `serve`. Maintenance must not share a
// lifecycle with the ingestion scheduler — repair re-embeds, and `serve` is the
// process operators are told to keep clear of embedding work once dedicated
// workers exist.
func runMaint(ctx context.Context, deps *ingestDeps) error {
	lg := zctx.From(ctx)
	cfg := deps.cfg.Maintenance

	s, err := maint.NewScheduler(deps.maintJobs(), maint.SchedulerOptions{
		DB:            deps.services.SQLDB,
		StartDelay:    cfg.StartDelay,
		DrainTimeout:  cfg.DrainTimeout,
		Logger:        lg,
		MeterProvider: deps.mp,
	})
	if err != nil {
		return err
	}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return s.Run(gctx) })

	if cfg.Addr != "" {
		mux := http.NewServeMux()
		mcpserver.InstallHealth(mux, deps.info.Short(), ingestHealthChecker{deps.services})
		srv := &http.Server{
			Addr:              cfg.Addr,
			Handler:           httpmw.Wrap(lg, deps.telemetry, mux),
			ReadHeaderTimeout: 10 * time.Second,
		}
		g.Go(func() error { return httpmw.Serve(gctx, lg, "http", srv) })
	}

	return g.Wait()
}

// maintJobs builds the scheduled jobs from config.
func (d *ingestDeps) maintJobs() []maint.Job {
	cfg := d.cfg.Maintenance
	return []maint.Job{
		{
			Name:     maint.JobGC,
			Interval: cfg.GC.Interval,
			Run:      d.gcJob(vectorgc.Options{Grace: cfg.GC.Grace, Batch: cfg.GC.Batch}),
		},
		{
			Name:     maint.JobRepair,
			Interval: cfg.Repair.Interval,
			Run:      d.repairJob(vectorrepair.Options{Batch: cfg.Repair.Batch}),
		},
		{
			Name:     maint.JobReapStale,
			Interval: cfg.ReapStale.Interval,
			Run: func(ctx context.Context) error {
				return maint.ReapStale(ctx, d.allQueues())
			},
		},
		{
			Name:     maint.JobQueueRetention,
			Interval: cfg.QueueRetention.Interval,
			Run: func(ctx context.Context) error {
				return maint.PurgeQueues(ctx, d.allQueues(), queue.PurgeOptions{
					DoneAfter:  cfg.QueueRetention.DoneAfter,
					ErrorAfter: cfg.QueueRetention.ErrorAfter,
					Batch:      cfg.QueueRetention.Batch,
					MaxBatches: cfg.QueueRetention.MaxBatches,
				})
			},
		},
		{
			Name:     maint.JobReconcile,
			Interval: cfg.Reconcile.Interval,
			Run: d.reconcileJob(reconcile.Options{
				MaxDeleteFraction:     cfg.Reconcile.MaxDeleteFraction,
				MinIndexedForFraction: cfg.Reconcile.MinIndexed,
			}),
		},
		{
			Name:     maint.JobNotifyRetention,
			Interval: cfg.NotifyRetention.Interval,
			Run: func(ctx context.Context) error {
				return maint.PurgeNotifications(ctx, notifystore.New(d.services.DB, notifystore.Options{}),
					notifystore.PurgeOptions{
						After:      cfg.NotifyRetention.After,
						Batch:      cfg.NotifyRetention.Batch,
						MaxBatches: cfg.NotifyRetention.MaxBatches,
					})
			},
		},
	}
}

// allQueues names every queue in the system, for the sweeps that are not about
// one queue's work but about the shared table underneath them all.
//
// Listing them here is the price of one table serving every queue: a new queue
// that is not added stays unreaped and unpurged, which is exactly how notify
// delivery ended up with no reaper. The queues are constructed per call because
// a queue.Postgres is a handle, not a connection.
func (d *ingestDeps) allQueues() []*queue.Postgres {
	names := []string{
		indexjob.QueueName,
		agentstore.QueueName,
		notifystore.QueueName(notify.ChannelTelegram),
	}

	qs := make([]*queue.Postgres, 0, len(names))
	for _, name := range names {
		qs = append(qs, queue.NewPostgres(d.services.DB, name, queue.PostgresOptions{}))
	}
	return qs
}

// vectorStore connects to Qdrant for the duration of one sweep.
//
// Maintenance deliberately does not use the store wire built at startup. That
// one is decided once: NewServices degrades to a nil store when Qdrant is
// unreachable, so a daemon that happened to start during an outage would never
// garbage-collect again, however long it ran afterwards. Resolving per sweep
// makes an outage cost one failed run that the next tick retries, which is the
// difference between a maintenance gap and a maintenance stoppage.
//
// The failure is reported as a failed run — logged, and counted by
// sisyphus.maint.runs{status="error"} — rather than by exiting: a Qdrant
// outage is not a reason to take the process down, and a restart would not fix
// it anyway.
func (d *ingestDeps) vectorStore(ctx context.Context) (*qdrant.Store, error) {
	return wire.NewVectorStore(ctx, d.cfg, d.services.Embedder)
}

// gcJob returns one vector garbage-collection sweep.
func (d *ingestDeps) gcJob(opts vectorgc.Options) func(context.Context) error {
	return func(ctx context.Context) error {
		store, err := d.vectorStore(ctx)
		if err != nil {
			return errors.Wrap(err, "gc: vector store")
		}
		defer func() { _ = store.Close() }()

		return maint.GC(ctx, store, vectorgc.NewEntRefStore(d.services.DB), opts)
	}
}

// repairJob returns one vector repair sweep.
func (d *ingestDeps) repairJob(opts vectorrepair.Options) func(context.Context) error {
	return func(ctx context.Context) error {
		if d.services.Embedder == nil {
			return errors.New("repair: embedder unavailable")
		}
		store, err := d.vectorStore(ctx)
		if err != nil {
			return errors.Wrap(err, "repair: vector store")
		}
		defer func() { _ = store.Close() }()

		return maint.Repair(ctx, d.services.DB, d.services.Embedder, store, opts)
	}
}

// runMaintOnce runs one job body under the same lock the scheduler uses, for
// the one-shot subcommands. A contended run is reported, not failed: the work
// is already being done.
func (d *ingestDeps) runMaintOnce(ctx context.Context, name string, fn func(context.Context) error) error {
	err := maint.WithLock(ctx, d.services.SQLDB, name, fn)
	if errors.Is(err, maint.ErrHeld) {
		zctx.From(ctx).Info("skipped: another process is already running this job",
			zap.String("job", name))
		return nil
	}
	return err
}
