package store

import (
	"context"
	stdsql "database/sql"
	"os"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/go-faster/errors"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/ent/queuejob"
	"github.com/go-faster/sisyphus/internal/notify"
)

// clock is an injectable time source, so lease and backoff behavior is
// testable without a real sleep.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock {
	return &clock{t: time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)}
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func openTestDB(t *testing.T) *ent.Client {
	t.Helper()
	dsn := os.Getenv("SISYPHUS_TEST_DB")
	if dsn == "" {
		t.Skip("SISYPHUS_TEST_DB not set")
	}

	db, err := stdsql.Open("pgx", dsn)
	require.NoError(t, err)
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	require.NoError(t, client.Schema.Create(ctx))
	t.Cleanup(func() {
		ctx := context.Background()
		// Scoped to this package's queues: the shared test database carries
		// other suites' jobs too.
		_, _ = client.QueueJob.Delete().Where(queuejob.QueueHasPrefix("notify.")).Exec(ctx)
		_, _ = client.Notification.Delete().Exec(ctx)
		_, _ = client.NotifySubscription.Delete().Exec(ctx)
		_, _ = client.UserToken.Delete().Exec(ctx)
		_, _ = client.User.Delete().Exec(ctx)
	})
	return client
}

func TestStore_EnrollLinkSubscribeRoundTrip(t *testing.T) {
	s := New(openTestDB(t), Options{})
	ctx := t.Context()

	const telegramUserID int64 = 1001
	userID, err := s.EnrollTelegram(ctx, telegramUserID, 555)
	require.NoError(t, err)
	require.NotEqual(t, userID.String(), "00000000-0000-0000-0000-000000000000")

	// Re-enrolling updates the access hash in place rather than erroring.
	userID2, err := s.EnrollTelegram(ctx, telegramUserID, 777)
	require.NoError(t, err)
	require.Equal(t, userID, userID2)

	require.NoError(t, s.LinkGitLab(ctx, telegramUserID, "alice"))
	require.NoError(t, s.LinkJira(ctx, telegramUserID, "acc-1", "Alice A"))

	require.NoError(t, s.Subscribe(ctx, telegramUserID, notify.SourceGitLab,
		[]notify.EventType{notify.EventMRAssigned, notify.EventMRReviewRequested}))

	subs, err := s.ListSubscriptions(ctx, telegramUserID)
	require.NoError(t, err)
	require.Len(t, subs, 1)
	require.Equal(t, notify.SourceGitLab, subs[0].Source)
	require.True(t, subs[0].Enabled)
	require.ElementsMatch(t, []notify.EventType{notify.EventMRAssigned, notify.EventMRReviewRequested}, subs[0].EventTypes)

	// Re-subscribing replaces the event type list instead of duplicating the row.
	require.NoError(t, s.Subscribe(ctx, telegramUserID, notify.SourceGitLab, []notify.EventType{notify.EventMRAssigned}))
	subs, err = s.ListSubscriptions(ctx, telegramUserID)
	require.NoError(t, err)
	require.Len(t, subs, 1)
	require.Equal(t, []notify.EventType{notify.EventMRAssigned}, subs[0].EventTypes)

	require.NoError(t, s.Unsubscribe(ctx, telegramUserID, notify.SourceGitLab))
	subs, err = s.ListSubscriptions(ctx, telegramUserID)
	require.NoError(t, err)
	require.Len(t, subs, 1)
	require.False(t, subs[0].Enabled)
}

func TestStore_SubscribersMatchesLinkedIdentity(t *testing.T) {
	s := New(openTestDB(t), Options{})
	ctx := t.Context()

	const telegramUserID int64 = 2002
	_, err := s.EnrollTelegram(ctx, telegramUserID, 999)
	require.NoError(t, err)
	require.NoError(t, s.LinkGitLab(ctx, telegramUserID, "bob"))
	require.NoError(t, s.Subscribe(ctx, telegramUserID, notify.SourceGitLab, []notify.EventType{notify.EventMRAssigned}))

	subs, err := s.Subscribers(ctx, notify.SourceGitLab, notify.EventMRAssigned, notify.Actor{Source: notify.SourceGitLab, Key: "bob"})
	require.NoError(t, err)
	require.Len(t, subs, 1)
	require.EqualValues(t, telegramUserID, subs[0].Target.TelegramUserID)
	require.EqualValues(t, 999, subs[0].Target.TelegramAccessHash)

	// Different event type: bob isn't subscribed to mr_review_requested.
	subs, err = s.Subscribers(ctx, notify.SourceGitLab, notify.EventMRReviewRequested, notify.Actor{Source: notify.SourceGitLab, Key: "bob"})
	require.NoError(t, err)
	require.Empty(t, subs)

	// Unknown username: no match.
	subs, err = s.Subscribers(ctx, notify.SourceGitLab, notify.EventMRAssigned, notify.Actor{Source: notify.SourceGitLab, Key: "nobody"})
	require.NoError(t, err)
	require.Empty(t, subs)
}

func TestStore_EnqueueDedupIsIdempotent(t *testing.T) {
	s := New(openTestDB(t), Options{})
	ctx := t.Context()

	const telegramUserID int64 = 3003
	userID, err := s.EnrollTelegram(ctx, telegramUserID, 111)
	require.NoError(t, err)

	target := notify.Target{TelegramUserID: telegramUserID, TelegramAccessHash: 111}
	n := notify.Notification{
		UserID:   userID,
		Source:   notify.SourceGitLab,
		Type:     notify.EventMRAssigned,
		Text:     "hello",
		URL:      "https://example.com/mr/1",
		DedupKey: notify.DedupKey(userID, "gitlab_mr_assign:group/proj!1:alice"),
	}

	created, err := s.Enqueue(ctx, notify.ChannelTelegram, target, n)
	require.NoError(t, err)
	require.True(t, created)

	// Same dedup key again: no-op, not an error.
	created, err = s.Enqueue(ctx, notify.ChannelTelegram, target, n)
	require.NoError(t, err)
	require.False(t, created)

	pending, err := s.Pending(ctx, notify.ChannelTelegram, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "hello", pending[0].Text)
}

func TestStore_AckDeliveredAndErrorTransitions(t *testing.T) {
	c := newClock()
	client := openTestDB(t)
	s := New(client, Options{Now: c.Now})
	ctx := t.Context()

	const telegramUserID int64 = 4004
	userID, err := s.EnrollTelegram(ctx, telegramUserID, 222)
	require.NoError(t, err)
	target := notify.Target{TelegramUserID: telegramUserID, TelegramAccessHash: 222}

	mkNotification := func(dedup string) notify.Notification {
		return notify.Notification{
			UserID:   userID,
			Source:   notify.SourceJira,
			Type:     notify.EventIssueAssigned,
			Text:     "issue assigned",
			DedupKey: notify.DedupKey(userID, dedup),
		}
	}

	// Delivered.
	_, err = s.Enqueue(ctx, notify.ChannelTelegram, target, mkNotification("issue-1"))
	require.NoError(t, err)
	pending, err := s.Pending(ctx, notify.ChannelTelegram, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.NoError(t, s.Ack(ctx, pending[0].ID, nil))

	delivered, err := client.Notification.Get(ctx, pending[0].ID)
	require.NoError(t, err)
	require.Equal(t, StatusDelivered, delivered.Status)
	require.NotNil(t, delivered.DeliveredAt)

	pending, err = s.Pending(ctx, notify.ChannelTelegram, 10)
	require.NoError(t, err)
	require.Empty(t, pending)

	// Failed repeatedly until MaxDeliveryAttempts. Each attempt is a fresh
	// claim, since a nacked delivery goes back to the queue rather than
	// staying in the caller's hand.
	_, err = s.Enqueue(ctx, notify.ChannelTelegram, target, mkNotification("issue-2"))
	require.NoError(t, err)

	var id uuid.UUID
	for attempt := 1; attempt <= MaxDeliveryAttempts; attempt++ {
		pending, err = s.Pending(ctx, notify.ChannelTelegram, 10)
		require.NoError(t, err)
		require.Len(t, pending, 1, "attempt %d", attempt)
		require.Equal(t, attempt, pending[0].Attempts)
		id = pending[0].ID
		require.NoError(t, s.Ack(ctx, id, errors.New("delivery failed")))
		// Past the retry backoff, so the next claim is not merely early.
		c.Advance(time.Hour)
	}

	pending, err = s.Pending(ctx, notify.ChannelTelegram, 10)
	require.NoError(t, err)
	require.Empty(t, pending, "attempts exhausted")

	failed, err := client.Notification.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, StatusError, failed.Status)
	require.Equal(t, MaxDeliveryAttempts, failed.Attempts)
	require.NotNil(t, failed.Error)
	require.Equal(t, "delivery failed", *failed.Error)
}

// TestStore_PendingLeasesExclusively is the property that lets a sink run
// more than one replica: a claimed delivery is invisible to every other
// drainer until its lease lapses.
func TestStore_PendingLeasesExclusively(t *testing.T) {
	c := newClock()
	client := openTestDB(t)
	s := New(client, Options{Now: c.Now, DeliveryLease: time.Minute})
	other := New(client, Options{Now: c.Now, DeliveryLease: time.Minute})
	ctx := t.Context()

	const telegramUserID int64 = 5005
	userID, err := s.EnrollTelegram(ctx, telegramUserID, 333)
	require.NoError(t, err)

	_, err = s.Enqueue(ctx, notify.ChannelTelegram,
		notify.Target{TelegramUserID: telegramUserID, TelegramAccessHash: 333},
		notify.Notification{
			UserID:   userID,
			Source:   notify.SourceGitLab,
			Type:     notify.EventMRAssigned,
			Text:     "only once",
			DedupKey: notify.DedupKey(userID, "lease-test"),
		})
	require.NoError(t, err)

	first, err := s.Pending(ctx, notify.ChannelTelegram, 10)
	require.NoError(t, err)
	require.Len(t, first, 1)

	second, err := other.Pending(ctx, notify.ChannelTelegram, 10)
	require.NoError(t, err)
	require.Empty(t, second, "a leased delivery must not be handed to a second drainer")

	// The first drainer dies without acking; the lease lapsing is what makes
	// the delivery recoverable.
	c.Advance(2 * time.Minute)
	retry, err := other.Pending(ctx, notify.ChannelTelegram, 10)
	require.NoError(t, err)
	require.Len(t, retry, 1)
	require.Equal(t, first[0].ID, retry[0].ID)
}
