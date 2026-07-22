package apiclient

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/api"
	"github.com/go-faster/sisyphus/internal/notify"
	notifystore "github.com/go-faster/sisyphus/internal/notify/store"
	"github.com/go-faster/sisyphus/internal/oas"
)

// fakeNotifyStore is an in-memory api.NotifyStore for round-tripping the
// notify endpoints through a real HTTP server/client, mirroring the rest of
// this file's fakeRetriever/fakeAnswerer pattern.
type fakeNotifyStore struct {
	gitlabLinks map[int64]string
	jiraLinks   map[int64]string
	subs        map[int64][]notifystore.Subscription
	pending     []notifystore.OutboxItem
	acked       map[uuid.UUID]error
}

func newFakeNotifyStore() *fakeNotifyStore {
	return &fakeNotifyStore{
		gitlabLinks: map[int64]string{},
		jiraLinks:   map[int64]string{},
		subs:        map[int64][]notifystore.Subscription{},
		acked:       map[uuid.UUID]error{},
	}
}

func (f *fakeNotifyStore) EnrollTelegram(_ context.Context, telegramUserID, _ int64) (uuid.UUID, error) {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte{byte(telegramUserID)}), nil
}

func (f *fakeNotifyStore) LinkGitLab(_ context.Context, telegramUserID int64, username string) error {
	f.gitlabLinks[telegramUserID] = username
	return nil
}

func (f *fakeNotifyStore) LinkJira(_ context.Context, telegramUserID int64, accountID, _ string) error {
	f.jiraLinks[telegramUserID] = accountID
	return nil
}

func (f *fakeNotifyStore) Subscribe(_ context.Context, telegramUserID int64, source notify.Source, eventTypes []notify.EventType) error {
	f.subs[telegramUserID] = []notifystore.Subscription{{Source: source, EventTypes: eventTypes, Enabled: true}}
	return nil
}

func (f *fakeNotifyStore) Unsubscribe(_ context.Context, telegramUserID int64, source notify.Source) error {
	for i, s := range f.subs[telegramUserID] {
		if s.Source == source {
			f.subs[telegramUserID][i].Enabled = false
		}
	}
	return nil
}

func (f *fakeNotifyStore) ListSubscriptions(_ context.Context, telegramUserID int64) ([]notifystore.Subscription, error) {
	return f.subs[telegramUserID], nil
}

func (f *fakeNotifyStore) Pending(_ context.Context, _ notify.Channel, limit int) ([]notifystore.OutboxItem, error) {
	if limit < len(f.pending) {
		return f.pending[:limit], nil
	}
	return f.pending, nil
}

func (f *fakeNotifyStore) Ack(_ context.Context, id uuid.UUID, deliverErr error) error {
	f.acked[id] = deliverErr
	return nil
}

func newNotifyTestServer(t *testing.T, store *fakeNotifyStore) (client *Client, closeServer func()) {
	t.Helper()
	handler := api.New(&fakeRetriever{}, &fakeAnswerer{}, "v1.0.0", api.WithNotifyStore(store))
	secHandler := api.NewSecurityHandler("secret-token")
	server, err := oas.NewServer(handler, secHandler, oas.WithErrorHandler(api.ErrorHandler))
	require.NoError(t, err)

	httpServer := httptest.NewServer(server)
	client, err = New(httpServer.URL, "secret-token", Options{})
	require.NoError(t, err)
	return client, httpServer.Close
}

func TestClientNotifyEnrollLinkSubscribeRoundTrip(t *testing.T) {
	store := newFakeNotifyStore()
	client, closeServer := newNotifyTestServer(t, store)
	defer closeServer()
	ctx := context.Background()

	require.NoError(t, client.NotifyEnroll(ctx, 1001, 555))
	require.NoError(t, client.NotifyLinkGitLab(ctx, 1001, "alice"))
	assert.Equal(t, "alice", store.gitlabLinks[1001])

	require.NoError(t, client.NotifyLinkJira(ctx, 1001, "acc-1", "Alice A"))
	assert.Equal(t, "acc-1", store.jiraLinks[1001])

	require.NoError(t, client.NotifySubscribe(ctx, 1001, "gitlab", []string{"mr_assigned"}))
	subs, err := client.NotifyListSubscriptions(ctx, 1001)
	require.NoError(t, err)
	require.Len(t, subs, 1)
	assert.Equal(t, "gitlab", subs[0].Source)
	assert.True(t, subs[0].Enabled)

	require.NoError(t, client.NotifyUnsubscribe(ctx, 1001, "gitlab"))
	subs, err = client.NotifyListSubscriptions(ctx, 1001)
	require.NoError(t, err)
	require.Len(t, subs, 1)
	assert.False(t, subs[0].Enabled)
}

func TestClientPendingAndAckNotifications(t *testing.T) {
	id := uuid.New()
	store := newFakeNotifyStore()
	store.pending = []notifystore.OutboxItem{
		{ID: id, TelegramUserID: 1001, TelegramAccessHash: 555, Text: "hello", URL: "https://example.com", Attempts: 0},
	}
	client, closeServer := newNotifyTestServer(t, store)
	defer closeServer()
	ctx := context.Background()

	pending, err := client.PendingNotifications(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, id, pending[0].ID)
	assert.Equal(t, "hello", pending[0].Text)
	assert.Equal(t, "https://example.com", pending[0].URL)

	require.NoError(t, client.AckNotification(ctx, id, nil))
	assert.NoError(t, store.acked[id])

	require.NoError(t, client.AckNotification(ctx, id, assert.AnError))
	require.Error(t, store.acked[id])
}

func TestClientNotifyEndpointsWithoutStoreReturn503(t *testing.T) {
	handler := api.New(&fakeRetriever{}, &fakeAnswerer{}, "v1.0.0")
	secHandler := api.NewSecurityHandler("secret-token")
	server, err := oas.NewServer(handler, secHandler, oas.WithErrorHandler(api.ErrorHandler))
	require.NoError(t, err)

	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	client, err := New(httpServer.URL, "secret-token", Options{})
	require.NoError(t, err)

	err = client.NotifyEnroll(context.Background(), 1, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "503")
}
