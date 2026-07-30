package webhook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-faster/errors"
	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/event"
)

// recordingRouter is a real event.Mux with one recording handler, so the
// handler is exercised against the router it actually runs against.
type recordingRouter struct {
	event.Router
	events []event.Event
	err    error
}

func newRecordingRouter(err error) *recordingRouter {
	r := &recordingRouter{Router: event.NewMux(), err: err}
	r.Subscribe(event.Subscription{
		Name: "recorder",
		Handler: event.HandlerFunc(func(_ context.Context, e event.Event) error {
			if r.err != nil {
				return r.err
			}
			r.events = append(r.events, e)
			return nil
		}),
	})
	return r
}

const alertBody = `{"version":"4","alerts":[{"status":"firing",
  "labels":{"alertname":"HighErrorRate","severity":"critical"},
  "startsAt":"2026-05-01T10:00:00Z","fingerprint":"abc123"}]}`

func post(t *testing.T, h http.Handler, body, auth string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhooks/alertmanager", strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAlertmanagerHandlerRoutes(t *testing.T) {
	router := newRecordingRouter(nil)
	h := NewAlertmanagerHandler(router, AlertmanagerOptions{})

	rec := post(t, h, alertBody, "")
	require.Equal(t, http.StatusAccepted, rec.Code)
	require.Len(t, router.events, 1)
	require.Equal(t, event.TypeAlertFiring, router.events[0].Type)
}

func TestAlertmanagerHandlerToken(t *testing.T) {
	for _, tt := range []struct {
		name string
		auth string
		want int
	}{
		{"valid", "Bearer s3cret", http.StatusAccepted},
		{"wrong token", "Bearer nope", http.StatusUnauthorized},
		{"missing", "", http.StatusUnauthorized},
		{"not bearer", "s3cret", http.StatusUnauthorized},
	} {
		t.Run(tt.name, func(t *testing.T) {
			router := newRecordingRouter(nil)
			h := NewAlertmanagerHandler(router, AlertmanagerOptions{Token: "s3cret"})
			require.Equal(t, tt.want, post(t, h, alertBody, tt.auth).Code)
		})
	}
}

func TestAlertmanagerHandlerInvalidBody(t *testing.T) {
	router := newRecordingRouter(nil)
	h := NewAlertmanagerHandler(router, AlertmanagerOptions{})
	require.Equal(t, http.StatusBadRequest, post(t, h, "{not json", "").Code)
	require.Empty(t, router.events)
}

// A routing failure must not be acked: Alertmanager retries on non-2xx, and
// handlers are idempotent on Event.ID, so a resend is cheaper than a drop.
func TestAlertmanagerHandlerRouteFailure(t *testing.T) {
	h := NewAlertmanagerHandler(newRecordingRouter(errors.New("boom")), AlertmanagerOptions{})
	require.Equal(t, http.StatusInternalServerError, post(t, h, alertBody, "").Code)
}
