package queue

import (
	"time"

	"github.com/cenkalti/backoff/v5"
)

// ExponentialBackoff doubles base per failed attempt, capped at maxDelay, with
// the jitter backoff/v5 applies by default: each delay is drawn from
// ±RandomizationFactor around the interval. Without that spread, every job
// nacked at the same attempt count becomes visible again at the same instant
// and the workers collide on the next claim.
//
// maxDelay caps the interval, not the jittered result, so a returned delay may
// exceed it by up to the randomization factor. That is deliberate: clamping
// would put every capped retry back at exactly maxDelay, reintroducing the
// collision at precisely the attempt counts where a backlog is worst.
//
// The returned function is attempt-indexed and safe for concurrent use, unlike
// backoff.ExponentialBackOff itself, which is stateful and documented as not
// thread-safe. Retry state here lives in the job row's attempts column rather
// than in the process, so each call builds its own sequence and walks it to the
// requested attempt — a handful of iterations, bounded by MaxAttempts.
func ExponentialBackoff(base, maxDelay time.Duration) func(attempt int) time.Duration {
	return func(attempt int) time.Duration {
		bo := &backoff.ExponentialBackOff{
			InitialInterval:     base,
			RandomizationFactor: backoff.DefaultRandomizationFactor,
			// Explicitly 2 rather than the library default of 1.5: this is the
			// doubling the queue's retry pacing was tuned around.
			Multiplier:  2,
			MaxInterval: maxDelay,
		}
		bo.Reset()

		var d time.Duration
		for range max(attempt, 1) {
			d = bo.NextBackOff()
		}
		return d
	}
}
