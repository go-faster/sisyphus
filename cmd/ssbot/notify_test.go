package main

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/api"
	"github.com/go-faster/sisyphus/internal/apiclient"
	"github.com/go-faster/sisyphus/internal/bot"
	"github.com/go-faster/sisyphus/internal/index"
	"github.com/go-faster/sisyphus/internal/notify"
	notifystore "github.com/go-faster/sisyphus/internal/notify/store"
	"github.com/go-faster/sisyphus/internal/oas"
)

type stubRetriever struct{}

func (stubRetriever) Retrieve(context.Context, index.Query) ([]index.Result, error) { return nil, nil }

type stubAnswerer struct{}

func (stubAnswerer) Answer(context.Context, index.Query, []index.Result) (index.Answer, error) {
	return index.Answer{}, nil
}

// fakeNotifyStore is a minimal in-memory api.NotifyStore backing the drain
// loop test: one pending notification, tracking whether it got acked.
type fakeNotifyStore struct {
	pending []notifystore.OutboxItem
	acked   map[uuid.UUID]error
	ackedN  int
	chats   []notifystore.Chat
	// onAck runs inside Ack, on the draining goroutine, so a test can act on
	// progress without racing the counter from a second one.
	onAck func()
}

func (f *fakeNotifyStore) EnrollTelegram(context.Context, int64) (uuid.UUID, error) {
	return uuid.Nil, nil
}

func (f *fakeNotifyStore) Subscribe(context.Context, int64, notify.Source, []notify.EventType) error {
	return nil
}
func (f *fakeNotifyStore) Unsubscribe(context.Context, int64, notify.Source) error { return nil }
func (f *fakeNotifyStore) ListSubscriptions(context.Context, int64) ([]notifystore.Subscription, error) {
	return nil, nil
}

func (f *fakeNotifyStore) Pending(_ context.Context, _ notify.Channel, limit int) ([]notifystore.OutboxItem, error) {
	if limit < len(f.pending) {
		return f.pending[:limit], nil
	}
	return f.pending, nil
}

func (f *fakeNotifyStore) Ack(_ context.Context, id uuid.UUID, deliverErr error) error {
	f.ackedN++
	f.acked[id] = deliverErr
	if f.onAck != nil {
		f.onAck()
	}
	return nil
}

func TestDrainPendingNotifications_DeliversAndAcks(t *testing.T) {
	id := uuid.New()
	store := &fakeNotifyStore{
		pending: []notifystore.OutboxItem{
			{ID: id, TelegramUserID: 1001, TelegramAccessHash: 555, Text: "hello", URL: "https://example.com", Attempts: 0},
		},
		acked: map[uuid.UUID]error{},
	}

	handler := api.New(stubRetriever{}, stubAnswerer{}, "v1.0.0", api.WithNotifyStore(store))
	secHandler := api.NewSecurityHandler("secret-token")
	server, err := oas.NewServer(handler, secHandler, oas.WithErrorHandler(api.ErrorHandler))
	require.NoError(t, err)
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	apiClient, err := apiclient.New(httpServer.URL, "secret-token", apiclient.Options{})
	require.NoError(t, err)

	// Silent bot: SendTo short-circuits to nil without needing a live
	// Telegram session, so the drain loop's fetch/deliver/ack wiring can be
	// exercised without MTProto.
	b := bot.New(context.Background(), stubRetriever{}, stubAnswerer{}, bot.BotCredentials{}, bot.BotOptions{
		Silent:         true,
		TracerProvider: otel.GetTracerProvider(),
		Logger:         zap.NewNop(),
		AllowedUserIDs: []int64{1},
	})

	drainPendingNotifications(context.Background(), zap.NewNop(), b, apiClient, 0)

	require.Equal(t, 1, store.ackedN)
	require.NoError(t, store.acked[id])
}

func (f *fakeNotifyStore) RegisterChat(_ context.Context, target notify.Target, title string, addedBy int64) error {
	f.chats = append(f.chats, notifystore.Chat{Target: target, Title: title, Enabled: true, AddedBy: addedBy})
	return nil
}

func (f *fakeNotifyStore) UnregisterChat(_ context.Context, target notify.Target) (bool, error) {
	for i, c := range f.chats {
		if c.Target.TelegramUserID == target.TelegramUserID {
			f.chats[i].Enabled = false
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeNotifyStore) ListChats(context.Context) ([]notifystore.Chat, error) { return f.chats, nil }

func TestNotifySendInterval(t *testing.T) {
	require.Equal(t, notify.DefaultSendInterval, notifySendInterval(0))
	require.Equal(t, 50*time.Millisecond, notifySendInterval(50))
	require.Zero(t, notifySendInterval(-1))
}

// The pause is between sends, not before the first, so a batch of n takes
// n-1 intervals. The assertion is a lower bound on elapsed time rather than an
// exact one: an upper bound would be a slow CI machine away from flaking.
func TestDrainPendingNotifications_PacesBatch(t *testing.T) {
	const send = 20 * time.Millisecond
	store := &fakeNotifyStore{acked: map[uuid.UUID]error{}}
	for range 3 {
		store.pending = append(store.pending, notifystore.OutboxItem{
			ID: uuid.New(), TelegramUserID: 1001, TelegramAccessHash: 555, Text: "hello",
		})
	}

	handler := api.New(stubRetriever{}, stubAnswerer{}, "v1.0.0", api.WithNotifyStore(store))
	secHandler := api.NewSecurityHandler("secret-token")
	server, err := oas.NewServer(handler, secHandler, oas.WithErrorHandler(api.ErrorHandler))
	require.NoError(t, err)
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	apiClient, err := apiclient.New(httpServer.URL, "secret-token", apiclient.Options{})
	require.NoError(t, err)
	b := bot.New(context.Background(), stubRetriever{}, stubAnswerer{}, bot.BotCredentials{}, bot.BotOptions{
		Silent:         true,
		TracerProvider: otel.GetTracerProvider(),
		Logger:         zap.NewNop(),
		AllowedUserIDs: []int64{1},
	})

	start := time.Now()
	drainPendingNotifications(context.Background(), zap.NewNop(), b, apiClient, send)

	require.Equal(t, 3, store.ackedN)
	require.GreaterOrEqual(t, time.Since(start), 2*send)
}

// A canceled context stops the batch mid-pause rather than sending the rest at
// shutdown.
func TestDrainPendingNotifications_PacingStopsOnCancel(t *testing.T) {
	store := &fakeNotifyStore{acked: map[uuid.UUID]error{}}
	for range 3 {
		store.pending = append(store.pending, notifystore.OutboxItem{
			ID: uuid.New(), TelegramUserID: 1001, TelegramAccessHash: 555, Text: "hello",
		})
	}

	handler := api.New(stubRetriever{}, stubAnswerer{}, "v1.0.0", api.WithNotifyStore(store))
	secHandler := api.NewSecurityHandler("secret-token")
	server, err := oas.NewServer(handler, secHandler, oas.WithErrorHandler(api.ErrorHandler))
	require.NoError(t, err)
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	apiClient, err := apiclient.New(httpServer.URL, "secret-token", apiclient.Options{})
	require.NoError(t, err)
	b := bot.New(context.Background(), stubRetriever{}, stubAnswerer{}, bot.BotCredentials{}, bot.BotOptions{
		Silent:         true,
		TracerProvider: otel.GetTracerProvider(),
		Logger:         zap.NewNop(),
		AllowedUserIDs: []int64{1},
	})

	// Canceled from inside the first ack rather than from a goroutine racing
	// the counter: the drain is what advances, so it is what should trip it.
	ctx, cancel := context.WithCancel(context.Background())
	store.onAck = cancel
	drainPendingNotifications(ctx, zap.NewNop(), b, apiClient, time.Hour)

	require.Equal(t, 1, store.ackedN)
}
