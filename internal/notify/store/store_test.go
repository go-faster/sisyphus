package store

import (
	"context"
	stdsql "database/sql"
	"os"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/go-faster/errors"
	"github.com/stretchr/testify/require"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/notify"
)

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
		_, _ = client.Notification.Delete().Exec(ctx)
		_, _ = client.NotifySubscription.Delete().Exec(ctx)
		_, _ = client.UserToken.Delete().Exec(ctx)
		_, _ = client.User.Delete().Exec(ctx)
	})
	return client
}

func TestStore_EnrollLinkSubscribeRoundTrip(t *testing.T) {
	s := New(openTestDB(t))
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
	s := New(openTestDB(t))
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
	s := New(openTestDB(t))
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
	s := New(openTestDB(t))
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

	pending, err = s.Pending(ctx, notify.ChannelTelegram, 10)
	require.NoError(t, err)
	require.Empty(t, pending)

	// Failed repeatedly until MaxDeliveryAttempts, then no longer pending.
	_, err = s.Enqueue(ctx, notify.ChannelTelegram, target, mkNotification("issue-2"))
	require.NoError(t, err)
	pending, err = s.Pending(ctx, notify.ChannelTelegram, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	id := pending[0].ID

	for range MaxDeliveryAttempts {
		require.NoError(t, s.Ack(ctx, id, errors.New("delivery failed")))
	}

	pending, err = s.Pending(ctx, notify.ChannelTelegram, 10)
	require.NoError(t, err)
	require.Empty(t, pending)
}
