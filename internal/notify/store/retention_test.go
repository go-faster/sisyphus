package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/ent/notification"
	"github.com/go-faster/sisyphus/internal/notify"
)

// writeNotification inserts a row directly in the given status, with updated_at
// set to when it settled. It bypasses Enqueue because retention cares about the
// row's age and status, not about how it got there.
func writeNotification(t *testing.T, client *ent.Client, key, status string, settledAt time.Time) {
	t.Helper()
	_, err := client.Notification.Create().
		SetDedupKey(key).
		SetChannel(string(notify.ChannelTelegram)).
		SetSource("test").
		SetEventType("test").
		SetText("hello").
		SetStatus(status).
		SetCreatedAt(settledAt).
		SetUpdatedAt(settledAt).
		Save(t.Context())
	require.NoError(t, err)
}

func countNotifications(t *testing.T, client *ent.Client) int {
	t.Helper()
	n, err := client.Notification.Query().Count(t.Context())
	require.NoError(t, err)
	return n
}

// TestPurgeDeletesSettledPastTheWindow pins the basic contract, measured from
// when the row settled.
func TestPurgeDeletesSettledPastTheWindow(t *testing.T) {
	client := openTestDB(t)
	s := New(client, Options{})
	c := newClock()

	writeNotification(t, client, "old-delivered", StatusDelivered, c.Now().Add(-100*24*time.Hour))
	writeNotification(t, client, "old-error", StatusError, c.Now().Add(-100*24*time.Hour))
	writeNotification(t, client, "recent-delivered", StatusDelivered, c.Now().Add(-time.Hour))

	rep, err := s.Purge(t.Context(), PurgeOptions{After: 90 * 24 * time.Hour, Now: c.Now})
	require.NoError(t, err)
	require.Equal(t, 2, rep.Deleted)
	require.False(t, rep.Capped)
	require.Equal(t, 1, countNotifications(t, client), "a row inside the window must survive")
}

// TestPurgeSparesPending is the safety property: a pending row is outstanding
// work however old, and deleting one strands the queue job that delivers it.
func TestPurgeSparesPending(t *testing.T) {
	client := openTestDB(t)
	s := New(client, Options{})
	c := newClock()

	writeNotification(t, client, "ancient-pending", StatusPending, c.Now().Add(-365*24*time.Hour))

	rep, err := s.Purge(t.Context(), PurgeOptions{After: time.Hour, Now: c.Now})
	require.NoError(t, err)
	require.Zero(t, rep.Deleted)
	require.Equal(t, 1, countNotifications(t, client))
}

// TestPurgeZeroWindowKeepsForever pins that 0 disables retention rather than
// deleting everything settled.
func TestPurgeZeroWindowKeepsForever(t *testing.T) {
	client := openTestDB(t)
	s := New(client, Options{})
	c := newClock()

	writeNotification(t, client, "ancient", StatusDelivered, c.Now().Add(-365*24*time.Hour))

	rep, err := s.Purge(t.Context(), PurgeOptions{After: 0, Now: c.Now})
	require.NoError(t, err)
	require.Zero(t, rep.Deleted)
	require.Equal(t, 1, countNotifications(t, client))
}

// TestPurgeCapsOneSweep pins that MaxBatches bounds one sweep and says so.
func TestPurgeCapsOneSweep(t *testing.T) {
	client := openTestDB(t)
	s := New(client, Options{})
	c := newClock()

	settled := c.Now().Add(-100 * 24 * time.Hour)
	for _, key := range []string{"a", "b", "c", "d"} {
		writeNotification(t, client, "capped-"+key, StatusDelivered, settled)
	}

	rep, err := s.Purge(t.Context(), PurgeOptions{
		After:      90 * 24 * time.Hour,
		Batch:      1,
		MaxBatches: 2,
		Now:        c.Now,
	})
	require.NoError(t, err)
	require.Equal(t, 2, rep.Deleted)
	require.True(t, rep.Capped)
	require.Equal(t, 2, countNotifications(t, client), "the rest waits for the next sweep")
}

// TestPurgeLeavesDeliveryJobsAlone pins the boundary between the two
// retentions: purging a notification row does not touch the queue, which has
// its own window and its own sweep.
func TestPurgeLeavesDeliveryJobsAlone(t *testing.T) {
	client := openTestDB(t)
	s := New(client, Options{})
	ctx := context.Background()
	c := newClock()

	target := notify.Target{TelegramUserID: 4242}
	created, err := s.Enqueue(ctx, notify.ChannelTelegram, target, notify.Notification{
		DedupKey: "retention-boundary",
		Source:   "test",
		Type:     "test",
		Text:     "hello",
	})
	require.NoError(t, err)
	require.True(t, created)

	// Settle the row far in the past, leaving its delivery job outstanding.
	_, err = client.Notification.Update().
		Where(notification.DedupKey("retention-boundary")).
		SetStatus(StatusDelivered).
		SetUpdatedAt(c.Now().Add(-365 * 24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	rep, err := s.Purge(ctx, PurgeOptions{After: 24 * time.Hour, Now: c.Now})
	require.NoError(t, err)
	require.Equal(t, 1, rep.Deleted)

	q, err := s.queue(notify.ChannelTelegram)
	require.NoError(t, err)
	pending, err := q.Fetch(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1, "the delivery job outlives the row it delivers")
}
