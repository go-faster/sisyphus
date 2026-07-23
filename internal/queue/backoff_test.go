package queue

import (
	"sync"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/stretchr/testify/require"
)

func TestExponentialBackoff(t *testing.T) {
	const (
		base     = time.Second
		maxDelay = 8 * time.Second
	)
	backoffFn := ExponentialBackoff(base, maxDelay)

	// Jitter makes the exact delay unpredictable by design, so assert the
	// interval it is drawn around: RetryInterval * (1 ± RandomizationFactor).
	for _, tt := range []struct {
		attempt  int
		interval time.Duration
	}{
		{attempt: 0, interval: time.Second},
		{attempt: 1, interval: time.Second},
		{attempt: 2, interval: 2 * time.Second},
		{attempt: 3, interval: 4 * time.Second},
		{attempt: 4, interval: 8 * time.Second},
		{attempt: 5, interval: 8 * time.Second},
		{attempt: 99, interval: 8 * time.Second},
	} {
		lo := time.Duration(float64(tt.interval) * (1 - backoff.DefaultRandomizationFactor))
		hi := time.Duration(float64(tt.interval) * (1 + backoff.DefaultRandomizationFactor))
		for range 20 {
			d := backoffFn(tt.attempt)
			require.GreaterOrEqual(t, d, lo, "attempt %d below jitter range", tt.attempt)
			require.LessOrEqual(t, d, hi, "attempt %d above jitter range", tt.attempt)
		}
	}
}

// TestExponentialBackoffJitters guards the property the library is here for: two
// jobs failing at the same attempt count must not come back at the same instant.
func TestExponentialBackoffJitters(t *testing.T) {
	backoffFn := ExponentialBackoff(time.Second, time.Minute)

	seen := make(map[time.Duration]struct{})
	for range 50 {
		seen[backoffFn(3)] = struct{}{}
	}
	require.Greater(t, len(seen), 1, "same attempt must not always yield the same delay")
}

// TestExponentialBackoffConcurrent pins that the returned function is safe to
// share across a worker's concurrent handlers, which backoff.ExponentialBackOff
// itself is not. Run with -race.
func TestExponentialBackoffConcurrent(t *testing.T) {
	backoffFn := ExponentialBackoff(time.Second, time.Minute)

	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			for range 50 {
				// t.Error, not require: FailNow may only be called from the
				// test goroutine.
				if d := backoffFn(i % 6); d <= 0 {
					t.Errorf("attempt %d: non-positive delay %s", i%6, d)
				}
			}
		})
	}
	wg.Wait()
}

func TestDeliveryLastAttempt(t *testing.T) {
	require.False(t, Delivery{Attempts: 1, MaxAttempts: 3}.LastAttempt())
	require.True(t, Delivery{Attempts: 3, MaxAttempts: 3}.LastAttempt())
	require.True(t, Delivery{Attempts: 4, MaxAttempts: 3}.LastAttempt())
}
