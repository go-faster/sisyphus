package webhook

import (
	"context"
	"crypto/subtle"
	"io"
	"net/http"
	"strings"

	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/event"
	"github.com/go-faster/sisyphus/internal/ingest/alertmanager"
)

// maxAlertBodyBytes caps one Alertmanager POST. A grouped notification holds
// tens of alerts, not megabytes, and the endpoint is reachable from wherever
// Alertmanager runs.
const maxAlertBodyBytes = 1 << 20

// AlertmanagerOptions configures [NewAlertmanagerHandler].
type AlertmanagerOptions struct {
	// Token, if set, is required as `Authorization: Bearer <token>`.
	// Alertmanager signs nothing, so a shared token is the only thing
	// standing between the endpoint and anyone who can reach it.
	Token  string
	Logger *zap.Logger
}

func (opts *AlertmanagerOptions) setDefaults() {
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
}

// NewAlertmanagerHandler returns an http.Handler that decodes an Alertmanager
// webhook into canonical events and routes them.
//
// It differs from the GitLab and Jira handlers on purpose: those only wake a
// fetcher, because the upstream REST API is the source of truth and the
// webhook body adds nothing. An alert has no such API to re-read — the POST
// *is* the occurrence — so this handler is itself the source adapter.
//
// Routing happens inline, before responding: Alertmanager retries on a
// non-2xx, so a 202 must mean the events reached their destinations.
func NewAlertmanagerHandler(router event.Router, opts AlertmanagerOptions) http.Handler {
	opts.setDefaults()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if opts.Token != "" && !bearerEquals(r.Header.Get("Authorization"), opts.Token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAlertBodyBytes))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}

		events, err := alertmanager.EventsFromWebhook(body)
		if err != nil {
			opts.Logger.Warn("decode alertmanager webhook", zap.Error(err))
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}

		if err := routeAll(r.Context(), router, events); err != nil {
			// Handlers are idempotent on Event.ID, so letting Alertmanager
			// resend the whole group is safe and better than dropping it.
			opts.Logger.Error("route alertmanager events", zap.Error(err), zap.Int("events", len(events)))
			http.Error(w, "route failed", http.StatusInternalServerError)
			return
		}

		opts.Logger.Debug("alertmanager webhook accepted", zap.Int("events", len(events)))
		w.WriteHeader(http.StatusAccepted)
	})
}

func routeAll(ctx context.Context, router event.Router, events []event.Event) error {
	for _, e := range events {
		if err := router.Route(ctx, e); err != nil {
			return err
		}
	}
	return nil
}

func bearerEquals(header, token string) bool {
	got, ok := strings.CutPrefix(header, "Bearer ")
	if !ok {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}
