package queue

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Handler processes one delivery. Returning nil acknowledges it; returning an
// error returns it to the queue for retry, or makes it terminal once its
// attempts are spent. A handler must be idempotent: the queue is
// at-least-once, so a job whose worker died after finishing but before
// acknowledging will run again.
type Handler func(ctx context.Context, d Delivery) error

// WorkerOptions configures a [Worker].
type WorkerOptions struct {
	// Concurrency caps deliveries in flight at once.
	Concurrency int
	// PollInterval is how long to wait before asking for work again after
	// finding none.
	PollInterval time.Duration
	Logger       *zap.Logger
}

func (opts *WorkerOptions) setDefaults() {
	if opts.Concurrency <= 0 {
		opts.Concurrency = 1
	}
	if opts.PollInterval == 0 {
		opts.PollInterval = time.Second
	}
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
}

// Worker drains a queue: claim, run, acknowledge, repeat. Running N of them —
// in one process or across replicas — is safe and is the intended way to
// scale, since claims are exclusive for the lease's duration.
type Worker struct {
	q    Queue
	h    Handler
	opts WorkerOptions
}

// NewWorker creates a worker that runs h for each delivery from q.
func NewWorker(q Queue, h Handler, opts WorkerOptions) *Worker {
	opts.setDefaults()
	return &Worker{q: q, h: h, opts: opts}
}

// Run drains until ctx is canceled, then waits for in-flight handlers to
// finish. It returns nil on clean shutdown: a queue error is logged and
// retried rather than killing the worker, since the usual cause is the
// database briefly going away.
func (w *Worker) Run(ctx context.Context) error {
	var (
		wg sync.WaitGroup
		// sem holds one token per busy slot. Claiming is gated on free slots
		// rather than on a concurrency-limited goroutine pool: a job must not
		// sit leased while it waits for a handler to start, or a backlog
		// burns lease time doing nothing.
		sem = make(chan struct{}, w.opts.Concurrency)
	)
	defer wg.Wait()

	for {
		// Block until at least one slot is free, then take whatever else is
		// free without waiting.
		select {
		case <-ctx.Done():
			return nil
		case sem <- struct{}{}:
		}
		free := 1
	grab:
		for free < w.opts.Concurrency {
			select {
			case sem <- struct{}{}:
				free++
			default:
				break grab
			}
		}

		batch, err := w.q.Fetch(ctx, free)
		if err != nil && ctx.Err() == nil {
			w.opts.Logger.Error("Fetch queue jobs", zap.Error(err))
		}
		for range free - len(batch) {
			<-sem
		}
		if len(batch) == 0 {
			if !w.wait(ctx) {
				return nil
			}
			continue
		}

		for _, d := range batch {
			wg.Go(func() {
				defer func() { <-sem }()
				w.run(ctx, d)
			})
		}
	}
}

func (w *Worker) run(ctx context.Context, d Delivery) {
	lg := w.opts.Logger.With(
		zap.Stringer("job_id", d.ID),
		zap.String("key", d.Key),
		zap.Int("attempt", d.Attempts),
	)

	// The handler gets exactly the claim's lifetime. Taking the deadline from
	// the delivery rather than from a configured timeout means a handler can
	// never still be running after its claim lapsed and another worker took
	// the job — there is no second knob to set inconsistently.
	jobCtx := ctx
	if !d.Deadline.IsZero() {
		var cancel context.CancelFunc
		jobCtx, cancel = context.WithDeadline(ctx, d.Deadline)
		defer cancel()
	}

	err := w.h(jobCtx, d)

	// Acknowledge even when the process is shutting down: a delivery whose
	// outcome is never recorded just waits out its lease and runs again.
	ackCtx := context.WithoutCancel(ctx)
	if err != nil {
		lg.Error("Job failed", zap.Error(err), zap.Bool("terminal", d.LastAttempt()))
		if nackErr := w.q.Nack(ackCtx, d.ID, err); nackErr != nil {
			lg.Error("Nack job", zap.Error(nackErr))
		}
		return
	}
	if ackErr := w.q.Ack(ackCtx, d.ID); ackErr != nil {
		lg.Error("Ack job", zap.Error(ackErr))
	}
}

// wait sleeps for the poll interval, reporting false if ctx ended first.
func (w *Worker) wait(ctx context.Context) bool {
	t := time.NewTimer(w.opts.PollInterval)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
