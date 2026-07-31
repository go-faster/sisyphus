package notify

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStalenessFresh(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	tests := []struct {
		name      string
		maxAge    time.Duration
		changedAt time.Time
		want      bool
	}{
		{
			// The source did not report a timestamp: notify rather than go
			// quiet, since dedup caps the cost of being wrong at one message.
			name:      "unknown time always passes",
			changedAt: time.Time{},
			want:      true,
		},
		{name: "just now", changedAt: now, want: true},
		{name: "within the default window", changedAt: now.Add(-23 * time.Hour), want: true},
		{name: "older than the default window", changedAt: now.Add(-25 * time.Hour)},
		{name: "exactly at the cutoff", maxAge: time.Hour, changedAt: now.Add(-time.Hour), want: true},
		{name: "just past a custom cutoff", maxAge: time.Hour, changedAt: now.Add(-time.Hour - time.Second)},
		{
			name:      "negative disables the check",
			maxAge:    -1,
			changedAt: now.AddDate(-1, 0, 0),
			want:      true,
		},
		{
			// A clock skew that puts the change in the future is not "old".
			name:      "future change",
			changedAt: now.Add(time.Hour),
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := Staleness{MaxAge: tt.maxAge, Now: clock}
			require.Equal(t, tt.want, s.Fresh(tt.changedAt))
		})
	}
}
