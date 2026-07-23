package queue

import "time"

// ExponentialBackoff doubles base per failed attempt, capped at max. The
// delay is deterministic: two workers retrying the same job at the same
// attempt count pick the same instant, which is fine here because only one of
// them can hold the claim.
func ExponentialBackoff(base, maxDelay time.Duration) func(attempt int) time.Duration {
	return func(attempt int) time.Duration {
		d := base
		for range max(attempt-1, 0) {
			d *= 2
			if d >= maxDelay {
				return maxDelay
			}
		}
		return min(d, maxDelay)
	}
}
