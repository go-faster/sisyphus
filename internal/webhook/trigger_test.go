package webhook

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTriggerDebouncesFire(t *testing.T) {
	t.Parallel()

	var runs atomic.Int64
	fired := make(chan struct{}, 1)
	trigger := NewTrigger(t.Context(), TriggerOptions{
		Window: 10 * time.Millisecond,
	})
	trigger.Register("gitlab", func(_ context.Context) error {
		runs.Add(1)
		fired <- struct{}{}
		return nil
	})
	defer trigger.Wait()

	for range 10 {
		trigger.Fire("gitlab")
	}

	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("trigger did not run")
	}

	// Give stale debounce timers a chance to run if they were not canceled.
	time.Sleep(30 * time.Millisecond)
	if got := runs.Load(); got != 1 {
		t.Fatalf("runs = %d, want 1", got)
	}
}

func TestTriggerRerunsWhenDirty(t *testing.T) {
	t.Parallel()

	started := make(chan struct{}, 2)
	releaseFirst := make(chan struct{})
	var runs atomic.Int64

	trigger := NewTrigger(t.Context(), TriggerOptions{
		Window: time.Nanosecond,
	})
	trigger.Register("jira", func(_ context.Context) error {
		run := runs.Add(1)
		started <- struct{}{}
		if run == 1 {
			<-releaseFirst
		}
		return nil
	})
	defer trigger.Wait()

	trigger.Fire("jira")
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first run did not start")
	}

	trigger.Fire("jira")
	close(releaseFirst)

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("dirty rerun did not start")
	}
	if got := runs.Load(); got != 2 {
		t.Fatalf("runs = %d, want 2", got)
	}
}

func TestTriggerConcurrentFire(t *testing.T) {
	t.Parallel()

	var runs atomic.Int64
	fired := make(chan struct{}, 1)
	trigger := NewTrigger(t.Context(), TriggerOptions{
		Window: 10 * time.Millisecond,
	})
	trigger.Register("gitlab", func(_ context.Context) error {
		runs.Add(1)
		select {
		case fired <- struct{}{}:
		default:
		}
		return nil
	})
	defer trigger.Wait()

	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			trigger.Fire("gitlab")
		})
	}
	wg.Wait()

	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("trigger did not run")
	}

	time.Sleep(30 * time.Millisecond)
	if got := runs.Load(); got != 1 {
		t.Fatalf("runs = %d, want 1", got)
	}
}

func TestTriggerWaitCancelsPendingTimer(t *testing.T) {
	t.Parallel()

	var runs atomic.Int64
	trigger := NewTrigger(t.Context(), TriggerOptions{
		Window: time.Hour,
	})
	trigger.Register("gitlab", func(_ context.Context) error {
		runs.Add(1)
		return nil
	})

	trigger.Fire("gitlab")
	trigger.Wait()

	time.Sleep(10 * time.Millisecond)
	if got := runs.Load(); got != 0 {
		t.Fatalf("runs = %d, want 0", got)
	}

	trigger.Fire("gitlab")
	time.Sleep(10 * time.Millisecond)
	if got := runs.Load(); got != 0 {
		t.Fatalf("runs after wait = %d, want 0", got)
	}
}

func TestTriggerPassesContext(t *testing.T) {
	t.Parallel()

	type contextKey struct{}
	ctx := context.WithValue(t.Context(), contextKey{}, "value")
	gotCtx := make(chan context.Context, 1)

	trigger := NewTrigger(ctx, TriggerOptions{
		Window: time.Nanosecond,
	})
	trigger.Register("gitlab", func(ctx context.Context) error {
		gotCtx <- ctx
		return nil
	})
	defer trigger.Wait()

	trigger.Fire("gitlab")

	select {
	case got := <-gotCtx:
		if got.Value(contextKey{}) != "value" {
			t.Fatalf("context value = %v, want value", got.Value(contextKey{}))
		}
	case <-time.After(time.Second):
		t.Fatal("trigger did not run")
	}
}
