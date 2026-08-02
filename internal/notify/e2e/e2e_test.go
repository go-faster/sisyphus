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
	"github.com/go-faster/sisyphus/internal/ent/queuejob"
	"github.com/go-faster/sisyphus/internal/event"
	"github.com/go-faster/sisyphus/internal/index"
	ingestgitlab "github.com/go-faster/sisyphus/internal/ingest/gitlab"
	"github.com/go-faster/sisyphus/internal/notify"
	notifygitlab "github.com/go-faster/sisyphus/internal/notify/gitlab"
	notifystore "github.com/go-faster/sisyphus/internal/notify/store"
	"github.com/go-faster/sisyphus/internal/oas"
	"github.com/go-faster/sisyphus/internal/tgpeer"
)

// fakeGitLabFetcher mocks the GitLab REST API boundary: one page, one MR,
// with a fixed assignee/reviewer set.
type fakeGitLabFetcher struct {
	refs []ingestgitlab.MergeRequestRef
}

func (f *fakeGitLabFetcher) FetchMergeRequests(_ context.Context, page int, cursor ingestgitlab.Cursor) ([]ingestgitlab.MergeRequestRef, ingestgitlab.Cursor, bool, error) {
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
	Buttons            []index.Link
}

// mockTelegramSink mocks the Telegram send boundary: instead of a real
// *bot.Bot.SendTo (which needs a live MTProto session), it just records
// what would have been sent. Safe for concurrent use, matching how a real
// drain loop might process deliveries.
type mockTelegramSink struct {
	mu        sync.Mutex
	delivered []deliveredMessage
}

func (m *mockTelegramSink) send(id uuid.UUID, userID, accessHash int64, text string, buttons []index.Link) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.delivered = append(m.delivered, deliveredMessage{
		NotificationID:     id,
		TelegramUserID:     userID,
		TelegramAccessHash: accessHash,
		Text:               text,
		Buttons:            buttons,
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
		sink.send(n.ID, n.TelegramUserID, n.TelegramAccessHash, n.Text, n.Buttons)
		require.NoError(t, apiClient.AckNotification(ctx, n.ID, nil))
	}
}

// collectMRs runs the GitLab source adapter's fetch-and-emit step the way
// ingestrun does: page through merge requests, turn each into its canonical
// event. Reimplemented here (rather than imported) because ingestrun.RunGitLab
// also needs an embedder and a vector store, which this test deliberately does
// not stand up — it is the notification half that is under test.
func collectMRs(ctx context.Context, t *testing.T, f *fakeGitLabFetcher, cursor ingestgitlab.Cursor) ([]event.Event, ingestgitlab.Cursor) {
	t.Helper()
	var events []event.Event
	page := 1
	for {
		refs, next, hasMore, err := f.FetchMergeRequests(ctx, page, cursor)
		require.NoError(t, err)
		for _, ref := range refs {
			e, err := ingestgitlab.EventFromMergeRequest(ref)
			require.NoError(t, err)
			events = append(events, e)
		}
		cursor = next
		if !hasMore {
			return events, cursor
		}
		page++
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
		_, _ = client.QueueJob.Delete().Where(queuejob.QueueHasPrefix("notify.")).Exec(ctx)
		_, _ = client.Notification.Delete().Exec(ctx)
		_, _ = client.NotifySubscription.Delete().Exec(ctx)
		_, _ = client.UserToken.Delete().Exec(ctx)
		_, _ = client.User.Delete().Exec(ctx)
	})
	return client
}

func TestE2E_GitLabMRAssignment_ToTelegramDelivery(t *testing.T) {
	db := openTestDB(t)
	ctx := t.Context()
	store := notifystore.New(db, notifystore.Options{})

	const telegramUserID int64 = 900100100
	const telegramAccessHash int64 = 555444333

	// --- Enrollment/linking/subscription, as the bot commands would do it.
	_, err := store.EnrollTelegram(ctx, telegramUserID)
	require.NoError(t, err)
	// The address lives with the peer now, recorded from an update rather
	// than from enrollment.
	_, err = tgpeer.New(db, tgpeer.Options{}).Upsert(ctx, []tgpeer.Peer{
		{Type: tgpeer.KindUser, ID: telegramUserID, AccessHash: telegramAccessHash},
	})
	require.NoError(t, err)
	_, err = store.SyncIdentities(ctx, []notifystore.Identity{
		{TelegramUserID: telegramUserID, GitLabUsername: "e2e-alice"},
	})
	require.NoError(t, err)
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
				Author:    chunkgitlab.User{Username: "e2e-dave"},
				WebURL:    "https://gitlab.example.com/group/project/-/merge_requests/42",
				Assignees: []string{"e2e-alice"},
				Reviewers: []string{"e2e-bob"},
				// Carol did the assigning, dave merely opened the MR: the
				// notification must name the former. The fetcher reads this
				// from the MR's system notes.
				AssignedBy:        chunkgitlab.User{Username: "e2e-carol"},
				AssignedAt:        updated,
				ReviewRequestedBy: chunkgitlab.User{Username: "e2e-carol"},
				ReviewRequestedAt: updated,
				Updated:           updated,
			},
		},
	}}
	// --- Real source adapter + real event router + real projector + real
	// Dispatcher + real Postgres-backed outbox. The adapter is the same one
	// knowledge-graph ingestion drives: one fetch, one event, both
	// destinations.
	events, cursor := collectMRs(ctx, t, fetcher, ingestgitlab.Cursor{})
	require.Len(t, events, 1, "one canonical mr.updated event (fan-out to recipients happens in the projector)")

	dispatcher := notify.NewDispatcher(store, store, notify.ChannelTelegram, nil)
	router := event.NewMux()
	router.Subscribe(event.Subscription{
		Name:    "notify-gitlab",
		Sources: []event.Source{event.SourceGitLab},
		// Clock pinned to the fixture, so the staleness cutoff measures
		// against the event rather than against wall-clock time.
		Handler: notify.NewRouterSubscriber(notifygitlab.Projector{
			Staleness: notify.Staleness{Now: func() time.Time { return updated }},
		}, dispatcher),
	})
	for _, e := range events {
		require.NoError(t, router.Route(ctx, e))
	}
	// The projector fans the MR out to alice (assignee) and bob (reviewer),
	// but only alice is enrolled/linked/subscribed, so only her mr_assigned
	// event produces an outbox row.

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
	// Ingested text is CommonMark-escaped: an actor named foo_bar_baz would
	// otherwise bleed italics across the rest of the line.
	require.Equal(t,
		"🔀 **e2e\\-carol** assigned you to "+
			"[MR \\!42\\: Fix flaky test](https://gitlab.example.com/group/project/-/merge_requests/42)",
		delivered[0].Text)
	// Buttons survive the whole path: projector -> outbox payload -> HTTP ->
	// the sink that renders them as an inline keyboard.
	require.Equal(t, []index.Link{
		{Text: "Open merge request", URL: "https://gitlab.example.com/group/project/-/merge_requests/42"},
	}, delivered[0].Buttons)

	// --- No re-delivery: the row is now delivered, draining again is a no-op.
	drainOnce(ctx, t, apiClient, sink)
	require.Len(t, sink.messages(), 1)

	// --- Re-emitting the same MR event (the fake source ignores the cursor and
	// returns it again) produces no new delivery: the outbox DedupKey — not a
	// collector-side diff — is what guarantees idempotence now.
	events2, _ := collectMRs(ctx, t, fetcher, cursor)
	require.Len(t, events2, 1)
	for _, e := range events2 {
		require.NoError(t, router.Route(ctx, e))
	}
	drainOnce(ctx, t, apiClient, sink)
	require.Len(t, sink.messages(), 1)
}

// The conversation half of the same pipeline: a comment on an MR that is
// already yours, all the way to a Telegram send. It shares every real
// component with the assignment test above; what is under test here is that
// a comment becomes its own delivery, keyed by the comment rather than by
// the object, and that the button opens the comment itself.
func TestE2E_GitLabMRComment_ToTelegramDelivery(t *testing.T) {
	db := openTestDB(t)
	ctx := t.Context()
	store := notifystore.New(db, notifystore.Options{})

	const telegramUserID int64 = 900100101
	const telegramAccessHash int64 = 555444334

	_, err := store.EnrollTelegram(ctx, telegramUserID)
	require.NoError(t, err)
	_, err = tgpeer.New(db, tgpeer.Options{}).Upsert(ctx, []tgpeer.Peer{
		{Type: tgpeer.KindUser, ID: telegramUserID, AccessHash: telegramAccessHash},
	})
	require.NoError(t, err)
	_, err = store.SyncIdentities(ctx, []notifystore.Identity{
		{TelegramUserID: telegramUserID, GitLabUsername: "e2e-alice"},
	})
	require.NoError(t, err)
	require.NoError(t, store.Subscribe(ctx, telegramUserID, notify.SourceGitLab,
		[]notify.EventType{notify.EventMRAssigned, notify.EventMRCommented}))

	updated := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	fetcher := &fakeGitLabFetcher{refs: []ingestgitlab.MergeRequestRef{
		{
			Project: "group/project",
			MR: chunkgitlab.MergeRequest{
				IID:       42,
				Title:     "Fix flaky test",
				Author:    chunkgitlab.User{Username: "e2e-dave"},
				WebURL:    "https://gitlab.example.com/group/project/-/merge_requests/42",
				Assignees: []string{"e2e-alice"},
				// Assigned months ago, so the assignment itself is history and
				// only the comment is news — which is the ordinary case for a
				// comment on work you already have.
				AssignedBy: chunkgitlab.User{Username: "e2e-carol"},
				AssignedAt: updated.AddDate(0, -3, 0),
				Threads: []chunkgitlab.Thread{{
					ID: "thread-1",
					Comments: []chunkgitlab.Comment{{
						ID:         "7",
						Author:     "e2e-carol",
						AuthorUser: chunkgitlab.User{Username: "e2e-carol"},
						Body:       "this still fails on CI",
						Created:    updated,
					}},
				}},
				Updated: updated,
			},
		},
	}}

	events, cursor := collectMRs(ctx, t, fetcher, ingestgitlab.Cursor{})
	require.Len(t, events, 1)

	dispatcher := notify.NewDispatcher(store, store, notify.ChannelTelegram, nil)
	router := event.NewMux()
	router.Subscribe(event.Subscription{
		Name:    "notify-gitlab",
		Sources: []event.Source{event.SourceGitLab},
		Handler: notify.NewRouterSubscriber(notifygitlab.Projector{
			Staleness: notify.Staleness{Now: func() time.Time { return updated }},
		}, dispatcher),
	})
	for _, e := range events {
		require.NoError(t, router.Route(ctx, e))
	}

	handler := api.New(nil, nil, "v1.0.0-e2e", api.WithNotifyStore(store))
	secHandler := api.NewSecurityHandler("secret-token")
	server, err := oas.NewServer(handler, secHandler, oas.WithErrorHandler(api.ErrorHandler))
	require.NoError(t, err)
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	apiClient, err := apiclient.New(httpServer.URL, "secret-token", apiclient.Options{})
	require.NoError(t, err)

	sink := &mockTelegramSink{}
	drainOnce(ctx, t, apiClient, sink)

	delivered := sink.messages()
	require.Len(t, delivered, 1, "the stale assignment is history; only the comment notifies")
	require.Equal(t, telegramUserID, delivered[0].TelegramUserID)
	require.Equal(t,
		"💬 **e2e\\-carol** commented on "+
			"[MR \\!42\\: Fix flaky test](https://gitlab.example.com/group/project/-/merge_requests/42#note_7)"+
			"\n\nthis still fails on CI",
		delivered[0].Text)
	// The button opens the comment, not just the MR: the anchor is a fragment
	// on the URL GitLab itself returned.
	require.Equal(t, []index.Link{
		{Text: "Open comment", URL: "https://gitlab.example.com/group/project/-/merge_requests/42#note_7"},
	}, delivered[0].Buttons)

	// Re-fetching the same MR re-emits the same comment, and the outbox dedup
	// key — keyed by the comment id, so an edit does not re-notify either —
	// makes it a no-op.
	events2, _ := collectMRs(ctx, t, fetcher, cursor)
	require.Len(t, events2, 1)
	for _, e := range events2 {
		require.NoError(t, router.Route(ctx, e))
	}
	drainOnce(ctx, t, apiClient, sink)
	require.Len(t, sink.messages(), 1)
}

// The terminal half: the MR lands and its author hears about it. What is new
// here is the recipient — the author is nobody's member set, so this is the
// one GitLab notification addressed by a field of the MR itself rather than by
// a membership — and that the merge is gated on merged_at rather than on
// having seen the state change, since "merged" stays true on every later poll.
func TestE2E_GitLabMRMerged_ToTelegramDelivery(t *testing.T) {
	db := openTestDB(t)
	ctx := t.Context()
	store := notifystore.New(db, notifystore.Options{})

	const telegramUserID int64 = 900100102
	const telegramAccessHash int64 = 555444335

	_, err := store.EnrollTelegram(ctx, telegramUserID)
	require.NoError(t, err)
	_, err = tgpeer.New(db, tgpeer.Options{}).Upsert(ctx, []tgpeer.Peer{
		{Type: tgpeer.KindUser, ID: telegramUserID, AccessHash: telegramAccessHash},
	})
	require.NoError(t, err)
	// The recipient is the MR's author this time, not a member of it.
	_, err = store.SyncIdentities(ctx, []notifystore.Identity{
		{TelegramUserID: telegramUserID, GitLabUsername: "e2e-dave"},
	})
	require.NoError(t, err)
	require.NoError(t, store.Subscribe(ctx, telegramUserID, notify.SourceGitLab,
		[]notify.EventType{notify.EventMRMerged}))

	updated := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	fetcher := &fakeGitLabFetcher{refs: []ingestgitlab.MergeRequestRef{
		{
			Project: "group/project",
			MR: chunkgitlab.MergeRequest{
				IID:    42,
				Title:  "Fix flaky test",
				Author: chunkgitlab.User{Username: "e2e-dave"},
				State:  "merged",
				WebURL: "https://gitlab.example.com/group/project/-/merge_requests/42",
				// Assigned months ago: the assignment is history, the merge is
				// the news.
				Assignees:  []string{"e2e-alice"},
				AssignedBy: chunkgitlab.User{Username: "e2e-carol"},
				AssignedAt: updated.AddDate(0, -3, 0),
				MergedAt:   updated,
				MergedBy:   chunkgitlab.User{Username: "e2e-carol"},
				Updated:    updated,
			},
		},
	}}

	events, cursor := collectMRs(ctx, t, fetcher, ingestgitlab.Cursor{})
	require.Len(t, events, 1)

	dispatcher := notify.NewDispatcher(store, store, notify.ChannelTelegram, nil)
	router := event.NewMux()
	router.Subscribe(event.Subscription{
		Name:    "notify-gitlab",
		Sources: []event.Source{event.SourceGitLab},
		Handler: notify.NewRouterSubscriber(notifygitlab.Projector{
			Staleness: notify.Staleness{Now: func() time.Time { return updated }},
		}, dispatcher),
	})
	for _, e := range events {
		require.NoError(t, router.Route(ctx, e))
	}

	handler := api.New(nil, nil, "v1.0.0-e2e", api.WithNotifyStore(store))
	secHandler := api.NewSecurityHandler("secret-token")
	server, err := oas.NewServer(handler, secHandler, oas.WithErrorHandler(api.ErrorHandler))
	require.NoError(t, err)
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	apiClient, err := apiclient.New(httpServer.URL, "secret-token", apiclient.Options{})
	require.NoError(t, err)

	sink := &mockTelegramSink{}
	drainOnce(ctx, t, apiClient, sink)

	delivered := sink.messages()
	require.Len(t, delivered, 1)
	require.Equal(t, telegramUserID, delivered[0].TelegramUserID)
	require.Equal(t,
		"🎉 **e2e\\-carol** merged "+
			"[MR \\!42\\: Fix flaky test](https://gitlab.example.com/group/project/-/merge_requests/42)",
		delivered[0].Text)
	require.Equal(t, []index.Link{
		{Text: "Open merge request", URL: "https://gitlab.example.com/group/project/-/merge_requests/42"},
	}, delivered[0].Buttons)

	// The MR stays merged forever, so every later poll re-states it. The
	// outbox dedup key — no timestamp in it, because an MR merges once — is
	// what keeps that one notification.
	events2, _ := collectMRs(ctx, t, fetcher, cursor)
	require.Len(t, events2, 1)
	for _, e := range events2 {
		require.NoError(t, router.Route(ctx, e))
	}
	drainOnce(ctx, t, apiClient, sink)
	require.Len(t, sink.messages(), 1)
}
