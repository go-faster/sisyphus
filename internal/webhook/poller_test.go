package webhook

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestPollerFiresImmediatelyAndOnInterval(t *testing.T) {
	t.Parallel()

	var runs atomic.Int64
	trigger := NewTrigger(t.Context(), TriggerOptions{
		Window: time.Millisecond,
	})
	trigger.Register("gitlab", func(_ context.Context) error {
		runs.Add(1)
		return nil
	})
	defer trigger.Wait()

	ctx, cancel := context.WithCancel(t.Context())
	poller := NewPoller(trigger, nil)
	poller.Start(ctx, "gitlab", 20*time.Millisecond)

	deadline := time.Now().Add(time.Second)
	for runs.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := runs.Load(); got < 3 {
		t.Fatalf("runs = %d, want at least 3", got)
	}

	cancel()
	poller.Wait()
}

func TestPollerDisabledForNonPositiveInterval(t *testing.T) {
	t.Parallel()

	trigger := NewTrigger(t.Context(), TriggerOptions{})
	var runs atomic.Int64
	trigger.Register("jira", func(_ context.Context) error {
		runs.Add(1)
		return nil
	})
	defer trigger.Wait()

	poller := NewPoller(trigger, nil)
	poller.Start(t.Context(), "jira", 0)
	poller.Wait()

	if got := runs.Load(); got != 0 {
		t.Fatalf("runs = %d, want 0", got)
	}
}
