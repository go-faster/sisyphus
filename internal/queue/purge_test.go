package queue

import (
	"testing"
	"time"

	"github.com/go-faster/errors"
	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/ent/queuejob"
)

// settle publishes n jobs and drives each to a terminal status, returning the
// queue with its clock parked at the moment they settled.
func settle(t *testing.T, q *Postgres, status string, n int) {
	t.Helper()
	ctx := t.Context()

	msgs := make([]Message, 0, n)
	for i := range n {
		msgs = append(msgs, Message{Key: status + "-" + string(rune('a'+i)), MaxAttempts: 1})
	}
	published, err := q.Publish(ctx, msgs...)
	require.NoError(t, err)
	require.Equal(t, n, published)

	claimed, err := q.Fetch(ctx, n)
	require.NoError(t, err)
	require.Len(t, claimed, n)

	for _, d := range claimed {
		if status == StatusDone {
			require.NoError(t, q.Ack(ctx, d.ID))
			continue
		}
		// MaxAttempts 1 means the first Nack is terminal.
		require.NoError(t, q.Nack(ctx, d.ID, errors.New("boom")))
	}
	require.Equal(t, n, countStatus(t, q, status))
}

func countStatus(t *testing.T, q *Postgres, status string) int {
	t.Helper()
	client, ok := q.db.(*ent.Client)
	require.True(t, ok)
	n, err := client.QueueJob.Query().
		Where(queuejob.Queue(q.name), queuejob.Status(status)).
		Count(t.Context())
	require.NoError(t, err)
	return n
}

// TestPurgeDeletesOnlyPastTheWindow pins that retention is measured from when a
// job settled, not from when it was created.
func TestPurgeDeletesOnlyPastTheWindow(t *testing.T) {
	q, _, c := testQueue(t, PostgresOptions{})
	settle(t, q, StatusDone, 3)

	// The clock is a Go-side override for the whole queue, so advancing it
	// moves "now" for the purge predicate exactly as elapsed time would.
	c.Advance(time.Hour)
	rep, err := q.Purge(t.Context(), PurgeOptions{DoneAfter: 24 * time.Hour})
	require.NoError(t, err)
	require.Zero(t, rep.Total(), "jobs inside the window must survive")
	require.Equal(t, 3, countStatus(t, q, StatusDone))

	c.Advance(25 * time.Hour)
	rep, err = q.Purge(t.Context(), PurgeOptions{DoneAfter: 24 * time.Hour})
	require.NoError(t, err)
	require.Equal(t, 3, rep.Done)
	require.Zero(t, countStatus(t, q, StatusDone))
}

// TestPurgeKeepsErrorsLonger is the point of the two windows: the rows an
// operator opens the table for outlive the ones nobody reads.
func TestPurgeKeepsErrorsLonger(t *testing.T) {
	q, _, c := testQueue(t, PostgresOptions{})
	settle(t, q, StatusDone, 2)
	settle(t, q, StatusError, 2)

	c.Advance(48 * time.Hour)
	rep, err := q.Purge(t.Context(), PurgeOptions{
		DoneAfter:  24 * time.Hour,
		ErrorAfter: 30 * 24 * time.Hour,
	})
	require.NoError(t, err)
	require.Equal(t, 2, rep.Done)
	require.Zero(t, rep.Error)
	require.Zero(t, countStatus(t, q, StatusDone))
	require.Equal(t, 2, countStatus(t, q, StatusError), "failed jobs must stay for inspection")
}

// TestPurgeSparesOutstandingWork pins the safety property: only terminal rows
// are eligible, so a job waiting out a long backoff is never deleted because it
// happens to be old.
func TestPurgeSparesOutstandingWork(t *testing.T) {
	q, _, c := testQueue(t, PostgresOptions{MaxAttempts: 5})
	ctx := t.Context()

	_, err := q.Publish(ctx, Message{Key: "pending"})
	require.NoError(t, err)
	claimed, err := q.Fetch(ctx, 1)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.NoError(t, q.Nack(ctx, claimed[0].ID, errors.New("retry me")))

	c.Advance(365 * 24 * time.Hour)
	rep, err := q.Purge(ctx, PurgeOptions{DoneAfter: time.Hour, ErrorAfter: time.Hour})
	require.NoError(t, err)
	require.Zero(t, rep.Total())
	require.Equal(t, 1, countStatus(t, q, StatusPending))
}

// TestPurgeZeroWindowKeepsForever pins that 0 disables a window rather than
// meaning "delete everything settled".
func TestPurgeZeroWindowKeepsForever(t *testing.T) {
	q, _, c := testQueue(t, PostgresOptions{})
	settle(t, q, StatusDone, 2)

	c.Advance(365 * 24 * time.Hour)
	rep, err := q.Purge(t.Context(), PurgeOptions{DoneAfter: 0, ErrorAfter: 0})
	require.NoError(t, err)
	require.Zero(t, rep.Total())
	require.Equal(t, 2, countStatus(t, q, StatusDone))
}

// TestPurgeCapsOneSweep pins that MaxBatches bounds the work and reports that
// it stopped early, so a neglected table cannot hold the maintenance lock for
// an unbounded time.
func TestPurgeCapsOneSweep(t *testing.T) {
	q, _, c := testQueue(t, PostgresOptions{})
	settle(t, q, StatusDone, 4)

	c.Advance(48 * time.Hour)
	rep, err := q.Purge(t.Context(), PurgeOptions{
		DoneAfter:  24 * time.Hour,
		Batch:      1,
		MaxBatches: 2,
	})
	require.NoError(t, err)
	require.Equal(t, 2, rep.Done)
	require.True(t, rep.Capped)
	require.Equal(t, 2, countStatus(t, q, StatusDone), "the rest waits for the next sweep")
}

// TestPurgeIgnoresOtherQueues pins that one queue's retention cannot delete
// another's rows — they share a table, and the sweeps run per queue.
func TestPurgeIgnoresOtherQueues(t *testing.T) {
	q, client, c := testQueue(t, PostgresOptions{})
	settle(t, q, StatusDone, 2)

	other := NewPostgres(client, q.name+".other", PostgresOptions{Now: c.Now})
	t.Cleanup(func() {
		_, _ = client.QueueJob.Delete().Where(queuejob.Queue(other.name)).Exec(t.Context())
	})
	settle(t, other, StatusDone, 2)

	c.Advance(48 * time.Hour)
	rep, err := q.Purge(t.Context(), PurgeOptions{DoneAfter: 24 * time.Hour})
	require.NoError(t, err)
	require.Equal(t, 2, rep.Done)
	require.Equal(t, 2, countStatus(t, other, StatusDone))
}
