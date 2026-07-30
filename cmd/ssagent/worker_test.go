package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/zap/zaptest"

	"github.com/go-faster/sisyphus/internal/agentstore"
	"github.com/go-faster/sisyphus/internal/event"
	"github.com/go-faster/sisyphus/internal/queue"
)

type capturingRouter struct{ events []event.Event }

func (r *capturingRouter) Route(_ context.Context, e event.Event) error {
	r.events = append(r.events, e)
	return nil
}

// An event-triggered investigation reports back onto the spine; a plain
// /investigate submission (no trigger) does not.
func TestInvestigateHandlerRoutesReport(t *testing.T) {
	for _, tt := range []struct {
		name      string
		trigger   *event.Event
		wantRoute bool
	}{
		{
			name: "alert-triggered",
			trigger: &event.Event{
				ID:      "alertmanager:abc:alert.firing",
				Source:  event.SourceAlertmanager,
				Type:    event.TypeAlertFiring,
				Subject: event.Ref{ID: "abc", Title: "HighErrorRate"},
			},
			wantRoute: true,
		},
		{name: "manual submission"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeJobStore()
			id := uuid.New()
			router := &capturingRouter{}

			payload, err := json.Marshal(agentstore.Payload{Description: "look into it", Trigger: tt.trigger})
			require.NoError(t, err)

			h := investigateHandler(store, &fakeInvestigator{}, router,
				noop.NewTracerProvider().Tracer(""), nil, zaptest.NewLogger(t))
			require.NoError(t, h(t.Context(), queue.Delivery{ID: id, Payload: payload, MaxAttempts: 2}))

			if !tt.wantRoute {
				require.Empty(t, router.events)
				return
			}
			require.Len(t, router.events, 1)
			require.Equal(t, event.TypeInvestigationCompleted, router.events[0].Type)
			require.Equal(t, "investigation:"+id.String(), router.events[0].ID)
			require.Equal(t, "HighErrorRate", router.events[0].Subject.Title)
		})
	}
}
