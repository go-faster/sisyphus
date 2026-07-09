// Package webhook provides debounced trigger and handler for provider webhooks.
package webhook

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
)

// entry tracks the debounce state for a single trigger key.
type entry struct {
	key string
	fn  func(context.Context) error
	lg  *zap.Logger
	ctx context.Context

	mu      sync.Mutex
	cond    *sync.Cond
	timer   *time.Timer
	timerID uint64
	running bool
	dirty   bool
	closed  bool
}

// TriggerOptions configures the debounce trigger.
type TriggerOptions struct {
	Logger *zap.Logger
	Window time.Duration
}

func (opts *TriggerOptions) setDefaults() {
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
	if opts.Window == 0 {
		opts.Window = 10 * time.Second
	}
}

// Trigger manages debounced execution of named functions. Fire(key) coalesces
// multiple calls into a single execution after Window; if Fire is called while
// the function is still running, it marks dirty and runs once more after completion.
type Trigger struct {
	mu     sync.Mutex
	byKey  map[string]*entry
	window time.Duration
	lg     *zap.Logger
	ctx    context.Context
}

// NewTrigger creates a new debounce Trigger.
func NewTrigger(ctx context.Context, opts TriggerOptions) *Trigger {
	if ctx == nil {
		ctx = context.Background()
	}
	opts.setDefaults()
	return &Trigger{
		byKey:  make(map[string]*entry),
		window: opts.Window,
		lg:     opts.Logger,
		ctx:    ctx,
	}
}

// Register associates a key with a callback. Must be called before Fire.
func (t *Trigger) Register(key string, fn func(context.Context) error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := &entry{
		key: key,
		fn:  fn,
		lg:  t.lg.With(zap.String("trigger", key)),
		ctx: t.ctx,
	}
	e.cond = sync.NewCond(&e.mu)
	t.byKey[key] = e
}

// Fire triggers a debounced execution for the given key. It resets the debounce
// window if no run is in progress, or marks dirty for a re-run after the
// current run finishes.
func (t *Trigger) Fire(key string) {
	t.mu.Lock()
	e, ok := t.byKey[key]
	t.mu.Unlock()
	if !ok {
		return
	}

	e.lg.Debug("fire received")

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}

	if e.running {
		e.dirty = true
		e.lg.Debug("trigger already running, marked dirty")
		return
	}

	if e.timer != nil {
		e.timer.Stop()
	}

	e.timerID++
	timerID := e.timerID
	e.timer = time.AfterFunc(t.window, func() {
		e.maybeRun(timerID)
	})
	e.lg.Debug("debounce timer reset", zap.Duration("window", t.window))
}

func (e *entry) maybeRun(timerID uint64) {
	e.mu.Lock()
	if e.closed || e.running || timerID != e.timerID {
		e.mu.Unlock()
		return
	}
	e.timer = nil
	e.running = true
	e.mu.Unlock()

	e.doRun()
}

func (e *entry) doRun() {
	for {
		e.lg.Info("trigger fired, running ingestion")

		if err := e.fn(e.ctx); err != nil {
			e.lg.Error("trigger execution failed", zap.Error(err))
		}

		e.mu.Lock()
		if e.closed || !e.dirty {
			e.running = false
			e.dirty = false
			e.cond.Broadcast()
			e.mu.Unlock()
			return
		}

		e.dirty = false
		e.mu.Unlock()
		e.lg.Info("rerunning trigger (was marked dirty during run)")
	}
}

// Wait blocks until all currently running and pending executions have finished.
// Call during shutdown to drain inflight work. Pending (debounced but not yet
// executing) timers are canceled and do not start.
func (t *Trigger) Wait() {
	t.mu.Lock()
	entries := make([]*entry, 0, len(t.byKey))
	for _, e := range t.byKey {
		entries = append(entries, e)
	}
	t.mu.Unlock()

	for _, e := range entries {
		e.mu.Lock()
		e.closed = true
		e.dirty = false
		if e.timer != nil {
			e.timer.Stop()
			e.timer = nil
		}
		for e.running {
			e.cond.Wait()
		}
		e.mu.Unlock()
	}
}
