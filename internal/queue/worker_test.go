package queue

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/go-faster/errors"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// fakeQueue serves a fixed backlog once, then reports empty forever, so a
// worker test terminates on its own without waiting on any real clock.
type fakeQueue struct {
	mu      sync.Mutex
	backlog []Delivery
	acked   []uuid.UUID
	nacked  map[uuid.UUID]string
	drained chan struct{}
	once    sync.Once
}

func newFakeQueue(backlog ...Delivery) *fakeQueue {
	return &fakeQueue{
		backlog: backlog,
		nacked:  map[uuid.UUID]string{},
		drained: make(chan struct{}),
	}
}

func (f *fakeQueue) Publish(context.Context, ...Message) (int, error) { return 0, nil }

func (f *fakeQueue) Fetch(_ context.Context, limit int) ([]Delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.backlog) == 0 {
		f.once.Do(func() { close(f.drained) })
		return nil, nil
	}
	n := min(limit, len(f.backlog))
	out := f.backlog[:n]
	f.backlog = f.backlog[n:]
	return out, nil
}

func (f *fakeQueue) Ack(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acked = append(f.acked, id)
	return nil
}

func (f *fakeQueue) Nack(_ context.Context, id uuid.UUID, cause error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nacked[id] = cause.Error()
	return nil
}

func runWorker(t *testing.T, q *fakeQueue, h Handler, opts WorkerOptions) {
	t.Helper()
	opts.PollInterval = time.Millisecond
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- NewWorker(q, h, opts).Run(ctx) }()

	select {
	case <-q.drained:
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not drain the backlog")
	}
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
}

func TestWorker_AcksSuccessNacksFailure(t *testing.T) {
	good, bad := Delivery{ID: uuid.New(), Attempts: 1, MaxAttempts: 3}, Delivery{ID: uuid.New(), Attempts: 1, MaxAttempts: 3}
	q := newFakeQueue(good, bad)

	runWorker(t, q, func(_ context.Context, d Delivery) error {
		if d.ID == bad.ID {
			return errors.New("handler failed")
		}
		return nil
	}, WorkerOptions{Concurrency: 2})

	require.Equal(t, []uuid.UUID{good.ID}, q.acked)
	require.Equal(t, map[uuid.UUID]string{bad.ID: "handler failed"}, q.nacked)
}

func TestWorker_RespectsConcurrency(t *testing.T) {
	const concurrency = 3
	backlog := make([]Delivery, 0, 12)
	for range 12 {
		backlog = append(backlog, Delivery{ID: uuid.New(), Attempts: 1, MaxAttempts: 3})
	}
	q := newFakeQueue(backlog...)

	var (
		mu       sync.Mutex
		inFlight int
		peak     int
	)
	runWorker(t, q, func(context.Context, Delivery) error {
		mu.Lock()
		inFlight++
		peak = max(peak, inFlight)
		mu.Unlock()
		defer func() {
			mu.Lock()
			inFlight--
			mu.Unlock()
		}()
		return nil
	}, WorkerOptions{Concurrency: concurrency})

	require.Len(t, q.acked, len(backlog))
	mu.Lock()
	defer mu.Unlock()
	require.LessOrEqual(t, peak, concurrency)
}

func TestWorker_JobTimeoutNacks(t *testing.T) {
	d := Delivery{ID: uuid.New(), Attempts: 1, MaxAttempts: 3}
	q := newFakeQueue(d)

	runWorker(t, q, func(ctx context.Context, _ Delivery) error {
		<-ctx.Done()
		return errors.Wrap(ctx.Err(), "job")
	}, WorkerOptions{Concurrency: 1, JobTimeout: time.Millisecond})

	require.Empty(t, q.acked)
	require.Contains(t, q.nacked[d.ID], "context deadline exceeded")
}

func TestWorker_AcksAfterShutdownSignal(t *testing.T) {
	// A handler that finishes as the process is stopping must still have its
	// outcome recorded, or the job waits out its lease and runs again.
	d := Delivery{ID: uuid.New(), Attempts: 1, MaxAttempts: 3}
	q := newFakeQueue(d)

	started := make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- NewWorker(q, func(ctx context.Context, _ Delivery) error {
			close(started)
			<-ctx.Done()
			return nil
		}, WorkerOptions{Concurrency: 1, PollInterval: time.Millisecond}).Run(ctx)
	}()

	<-started
	cancel()
	require.NoError(t, <-done)
	require.Equal(t, []uuid.UUID{d.ID}, q.acked)
}
