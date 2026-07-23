package queue

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestExponentialBackoff(t *testing.T) {
	backoff := ExponentialBackoff(time.Second, 8*time.Second)
	for _, tt := range []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 0, want: time.Second},
		{attempt: 1, want: time.Second},
		{attempt: 2, want: 2 * time.Second},
		{attempt: 3, want: 4 * time.Second},
		{attempt: 4, want: 8 * time.Second},
		{attempt: 5, want: 8 * time.Second},
		{attempt: 99, want: 8 * time.Second},
	} {
		require.Equal(t, tt.want, backoff(tt.attempt), "attempt %d", tt.attempt)
	}
}

func TestDeliveryLastAttempt(t *testing.T) {
	require.False(t, Delivery{Attempts: 1, MaxAttempts: 3}.LastAttempt())
	require.True(t, Delivery{Attempts: 3, MaxAttempts: 3}.LastAttempt())
	require.True(t, Delivery{Attempts: 4, MaxAttempts: 3}.LastAttempt())
}
