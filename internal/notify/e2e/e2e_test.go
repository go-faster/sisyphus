// Package e2e_test exercises the full notification pipeline — GitLab
// collector -> Dispatcher -> Postgres outbox -> ssapi HTTP endpoints ->
// apiclient -> delivery -> ack -> no re-delivery — with only the two
// external boundaries mocked: the GitLab REST API (a fake
// notifygitlab.Fetcher) and the Telegram send (a fake sink recording what
// would have been delivered, standing in for internal/bot.Bot.SendTo/gotd).
// Everything in between (ent/Postgres, the dispatcher, the ogen HTTP
// handlers, the apiclient wire format) is real.
package e2e_test

import (
	"context"
	stdsql "database/sql"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/go-faster/sisyphus/internal/api"
	"github.com/go-faster/sisyphus/internal/apiclient"
	chunkgitlab "github.com/go-faster/sisyphus/internal/chunk/gitlab"
	"github.com/go-faster/sisyphus/internal/ent"
	ingestgitlab "github.com/go-faster/sisyphus/internal/ingest/gitlab"
	"github.com/go-faster/sisyphus/internal/notify"
	notifygitlab "github.com/go-faster/sisyphus/internal/notify/gitlab"
	notifystore "github.com/go-faster/sisyphus/internal/notify/store"
	"github.com/go-faster/sisyphus/internal/oas"
)

// fakeGitLabFetcher mocks the GitLab REST API boundary: one page, one MR,
// with a fixed assignee/reviewer set.
type fakeGitLabFetcher struct {
	refs []ingestgitlab.MergeRequestRef
}

func (f *fakeGitLabFetcher) FetchMergeRequestsStructured(_ context.Context, page int, cursor ingestgitlab.Cursor) ([]ingestgitlab.MergeRequestRef, ingestgitlab.Cursor, bool, error) {
	if page > 1 {
		return nil, cursor, false, nil
	}
	var maxUpdated string
	for _, r := range f.refs {
		if u := r.MR.Updated.Format(time.RFC3339); u > maxUpdated {
			maxUpdated = u
		}
	}
	return f.refs, ingestgitlab.Cursor{UpdatedAfter: maxUpdated}, false, nil
}

// deliveredMessage is one call the mock Telegram sink recorded.
type deliveredMessage struct {
	NotificationID     uuid.UUID
	TelegramUserID     int64
	TelegramAccessHash int64
	Text               string
}

// mockTelegramSink mocks the Telegram send boundary: instead of a real
// *bot.Bot.SendTo (which needs a live MTProto session), it just records
// what would have been sent. Safe for concurrent use, matching how a real
// drain loop might process deliveries.
type mockTelegramSink struct {
	mu        sync.Mutex
	delivered []deliveredMessage
}

func (m *mockTelegramSink) send(id uuid.UUID, userID, accessHash int64, text string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.delivered = append(m.delivered, deliveredMessage{
		NotificationID:     id,
		TelegramUserID:     userID,
		TelegramAccessHash: accessHash,
		Text:               text,
	})
}

func (m *mockTelegramSink) messages() []deliveredMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]deliveredMessage(nil), m.delivered...)
}

// drainOnce mirrors cmd/ssbot's drainPendingNotifications: fetch pending
// Telegram-channel notifications, "deliver" each via the mock sink, ack the
// outcome. Reimplemented here (rather than imported) because that function
// lives in package main; this exercises the exact same apiclient contract
// ssbot's real drain loop does.
func drainOnce(ctx context.Context, t *testing.T, apiClient *apiclient.Client, sink *mockTelegramSink) {
	t.Helper()
	pending, err := apiClient.PendingNotifications(ctx, 20)
	require.NoError(t, err)
	for _, n := range pending {
		sink.send(n.ID, n.TelegramUserID, n.TelegramAccessHash, n.Text)
		require.NoError(t, apiClient.AckNotification(ctx, n.ID, nil))
	}
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
		_, _ = client.Notification.Delete().Exec(ctx)
		_, _ = client.NotifySubscription.Delete().Exec(ctx)
		_, _ = client.NotifyUser.Delete().Exec(ctx)
	})
	return client
}

func TestE2E_GitLabMRAssignment_ToTelegramDelivery(t *testing.T) {
	db := openTestDB(t)
	ctx := t.Context()
	store := notifystore.New(db)

	const telegramUserID int64 = 900100100
	const telegramAccessHash int64 = 555444333

	// --- Enrollment/linking/subscription, as the bot commands would do it.
	_, err := store.EnrollTelegram(ctx, telegramUserID, telegramAccessHash)
	require.NoError(t, err)
	require.NoError(t, store.LinkGitLab(ctx, telegramUserID, "e2e-alice"))
	require.NoError(t, store.Subscribe(ctx, telegramUserID, notify.SourceGitLab,
		[]notify.EventType{notify.EventMRAssigned, notify.EventMRReviewRequested}))

	// --- Mocked GitLab source: one MR, alice newly assigned, bob newly a reviewer.
	updated := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	fetcher := &fakeGitLabFetcher{refs: []ingestgitlab.MergeRequestRef{
		{
			Project: "group/project",
			MR: chunkgitlab.MergeRequest{
				IID:       42,
				Title:     "Fix flaky test",
				Author:    "e2e-carol",
				WebURL:    "https://gitlab.example.com/group/project/-/merge_requests/42",
				Assignees: []string{"e2e-alice"},
				Reviewers: []string{"e2e-bob"},
				Updated:   updated,
			},
		},
	}}
	collector := notifygitlab.New(fetcher)

	// --- Real collector + real Dispatcher + real Postgres-backed outbox.
	events, cursor, err := collector.Collect(ctx, "")
	require.NoError(t, err)
	require.Len(t, events, 2, "one mr_assigned (alice) + one mr_review_requested (bob, not subscribed)")

	dispatcher := notify.NewDispatcher(store, store, notify.ChannelTelegram, nil)
	enqueued, err := dispatcher.Dispatch(ctx, events)
	require.NoError(t, err)
	// Only alice is enrolled/linked/subscribed; bob has no NotifyUser row,
	// so only alice's mr_assigned event produces an outbox row.
	require.Equal(t, 1, enqueued)

	// --- Real ssapi HTTP handler + apiclient, backed by the same store.
	handler := api.New(nil, nil, "v1.0.0-e2e", api.WithNotifyStore(store))
	secHandler := api.NewSecurityHandler("secret-token")
	server, err := oas.NewServer(handler, secHandler, oas.WithErrorHandler(api.ErrorHandler))
	require.NoError(t, err)
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	apiClient, err := apiclient.New(httpServer.URL, "secret-token", apiclient.Options{})
	require.NoError(t, err)

	// --- Mocked Telegram sink drain, over the real HTTP API.
	sink := &mockTelegramSink{}
	drainOnce(ctx, t, apiClient, sink)

	delivered := sink.messages()
	require.Len(t, delivered, 1)
	require.Equal(t, telegramUserID, delivered[0].TelegramUserID)
	require.Equal(t, telegramAccessHash, delivered[0].TelegramAccessHash)
	require.Contains(t, delivered[0].Text, "Fix flaky test")
	require.Contains(t, delivered[0].Text, "e2e-carol")

	// --- No re-delivery: the row is now delivered, draining again is a no-op.
	drainOnce(ctx, t, apiClient, sink)
	require.Len(t, sink.messages(), 1)

	// --- Re-running the collector with the advanced cursor against the same
	// (unchanged) MR emits nothing new: idempotence at the collector layer too.
	events2, _, err := collector.Collect(ctx, cursor)
	require.NoError(t, err)
	require.Empty(t, events2)
}
