// Package maint runs periodic maintenance jobs — vector garbage collection,
// vector repair — on a schedule, one at a time across processes.
//
// It exists because the target architecture's CronJobs are not available in
// every deployment: compose has no such object, so the schedule has to live in
// a process. [Scheduler] is that process's timer, and a Postgres advisory lock
// per job takes the place of `concurrencyPolicy: Forbid`. The one-shot CLI
// subcommands call the same job bodies, so scheduling a job and running it by
// hand execute identical code and cannot overlap.
package maint

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/pglock"
)

// Job is one periodic maintenance task.
type Job struct {
	// Name identifies the job in logs, metrics and its lock key.
	Name string
	// Interval is how often Run fires. Zero or negative disables the job.
	Interval time.Duration
	// Run performs one pass. It must be idempotent and safe to cancel: a run
	// interrupted by shutdown is abandoned, not awaited, and the next one
	// re-finds whatever was left.
	Run func(ctx context.Context) error
}

// ErrHeld reports that another process is already running the job, so this run
// was skipped.
var ErrHeld = pglock.ErrHeld

// WithLock runs fn under the advisory lock for the named job, returning
// [ErrHeld] if another process holds it.
//
// Both the scheduler and the CLI subcommands go through here, so a hand-run
// `ssingest gc` and a scheduled one are mutually exclusive.
func WithLock(ctx context.Context, db *sql.DB, name string, fn func(context.Context) error) error {
	return pglock.With(ctx, db, "maint/"+name, fn)
}

// SchedulerOptions configures a [Scheduler].
type SchedulerOptions struct {
	// DB is the pooled handle the per-job advisory lock is taken on. A nil DB
	// runs jobs unlocked, matching pglock's own fallback.
	DB *sql.DB
	// StartDelay is how long after startup the first pass of each job fires.
	//
	// Jobs deliberately do not wait a full Interval for their first run: a
	// deployment that restarts daily would never reach a daily job. The delay
	// is what keeps that from turning into a crash-loop hammering Qdrant on
	// every restart.
	StartDelay time.Duration
	// DrainTimeout bounds how long Run waits for in-flight jobs after its
	// context is canceled.
	DrainTimeout time.Duration

	Logger        *zap.Logger
	MeterProvider metric.MeterProvider
}

func (opts *SchedulerOptions) setDefaults() {
	if opts.StartDelay == 0 {
		opts.StartDelay = 5 * time.Minute
	}
	if opts.DrainTimeout == 0 {
		opts.DrainTimeout = 30 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
	if opts.MeterProvider == nil {
		opts.MeterProvider = otel.GetMeterProvider()
	}
}

// Scheduler fires a set of jobs on their own intervals until its context ends.
type Scheduler struct {
	jobs    []Job
	opts    SchedulerOptions
	metrics *metrics
}

// NewScheduler builds a Scheduler over jobs. Jobs with a non-positive interval
// are dropped, so a config that disables everything yields a scheduler that
// does nothing rather than an error.
func NewScheduler(jobs []Job, opts SchedulerOptions) (*Scheduler, error) {
	opts.setDefaults()

	enabled := make([]Job, 0, len(jobs))
	seen := make(map[string]bool, len(jobs))
	for _, j := range jobs {
		if j.Name == "" {
			return nil, errors.New("maint: job name required")
		}
		if seen[j.Name] {
			return nil, errors.Errorf("maint: duplicate job %q", j.Name)
		}
		seen[j.Name] = true
		if j.Run == nil {
			return nil, errors.Errorf("maint: job %q has no Run", j.Name)
		}
		if j.Interval <= 0 {
			opts.Logger.Info("maintenance job disabled", zap.String("job", j.Name))
			continue
		}
		enabled = append(enabled, j)
	}

	m, err := newMetrics(opts.MeterProvider)
	if err != nil {
		// A metrics setup error must not stop maintenance from running, the
		// same trade internal/webhook makes.
		opts.Logger.Warn("maint metrics setup failed, metrics disabled", zap.Error(err))
		m = nil
	}

	return &Scheduler{jobs: enabled, opts: opts, metrics: m}, nil
}

// Jobs reports the enabled jobs, for logging what a process will actually do.
func (s *Scheduler) Jobs() []Job { return s.jobs }

// Run fires every enabled job on its interval until ctx is canceled, then waits
// up to DrainTimeout for in-flight passes to return.
//
// A failing job is logged and retried on its next tick; it never stops the
// scheduler, because one broken sweep must not take the others down with it.
func (s *Scheduler) Run(ctx context.Context) error {
	lg := s.opts.Logger
	if len(s.jobs) == 0 {
		lg.Warn("no maintenance jobs enabled, idling")
		<-ctx.Done()
		return nil
	}

	var wg sync.WaitGroup
	for _, j := range s.jobs {
		wg.Go(func() { s.loop(ctx, j) })
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		wg.Wait()
	}()

	<-ctx.Done()

	drain := time.NewTimer(s.opts.DrainTimeout)
	defer drain.Stop()
	select {
	case <-done:
	case <-drain.C:
		// The process is on its way out and the jobs hold no state a caller
		// waits on; say so rather than block the shutdown indefinitely.
		lg.Warn("maintenance jobs still running at drain deadline, exiting anyway",
			zap.Duration("timeout", s.opts.DrainTimeout))
	}
	return nil
}

func (s *Scheduler) loop(ctx context.Context, j Job) {
	ctx = zctx.With(ctx, zap.String("job", j.Name))
	lg := s.opts.Logger.With(zap.String("job", j.Name), zap.Duration("interval", j.Interval))
	lg.Info("maintenance job scheduled", zap.Duration("start_delay", s.opts.StartDelay))

	delay := time.NewTimer(s.opts.StartDelay)
	defer delay.Stop()
	select {
	case <-ctx.Done():
		return
	case <-delay.C:
	}

	ticker := time.NewTicker(j.Interval)
	defer ticker.Stop()
	for {
		s.runOnce(ctx, j, lg)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Scheduler) runOnce(ctx context.Context, j Job, lg *zap.Logger) {
	start := time.Now()
	err := WithLock(ctx, s.opts.DB, j.Name, j.Run)
	dur := time.Since(start)

	switch {
	case errors.Is(err, pglock.ErrHeld):
		// Another process is already doing this work. Expected, not a failure.
		s.metrics.recordRun(ctx, j.Name, statusSkipped, dur.Seconds())
		lg.Info("maintenance job skipped, running elsewhere")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		s.metrics.recordRun(ctx, j.Name, statusCanceled, dur.Seconds())
		lg.Info("maintenance job canceled", zap.Duration("took", dur))
	case err != nil:
		s.metrics.recordRun(ctx, j.Name, statusError, dur.Seconds())
		lg.Error("maintenance job failed", zap.Error(err), zap.Duration("took", dur))
	default:
		s.metrics.recordRun(ctx, j.Name, statusOK, dur.Seconds())
		lg.Info("maintenance job finished", zap.Duration("took", dur))
	}
}
