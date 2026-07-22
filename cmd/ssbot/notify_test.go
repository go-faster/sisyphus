package main

import (
	"context"
	"net/http/httptest"
	"testing"

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
}

func (f *fakeNotifyStore) EnrollTelegram(context.Context, int64, int64) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (f *fakeNotifyStore) LinkGitLab(context.Context, int64, string) error       { return nil }
func (f *fakeNotifyStore) LinkJira(context.Context, int64, string, string) error { return nil }
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

	drainPendingNotifications(context.Background(), zap.NewNop(), b, apiClient)

	require.Equal(t, 1, store.ackedN)
	require.NoError(t, store.acked[id])
}
