package alert

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/event"
	ingestalert "github.com/go-faster/sisyphus/internal/ingest/alertmanager"
	"github.com/go-faster/sisyphus/internal/notify"
)

func firingEvent(t *testing.T) event.Event {
	t.Helper()
	e := event.Event{
		ID:         "alertmanager:abc:alert.firing:2026-05-01T10:00:00Z",
		Source:     event.SourceAlertmanager,
		Type:       event.TypeAlertFiring,
		Subject:    event.Ref{ID: "abc", Title: "HighErrorRate: 5xx above 5%", URL: "https://prometheus.example.com/graph"},
		Severity:   event.SeverityCritical,
		OccurredAt: time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC),
	}
	e, err := e.WithPayload(ingestalert.AlertPayload{
		Labels:      map[string]string{"severity": "critical", "service": "checkout", "pod": "checkout-7d9"},
		Annotations: map[string]string{"description": "error ratio 12%", "runbook_url": "https://runbooks.example.com/high-error-rate"},
	})
	require.NoError(t, err)
	return e
}

func TestProjectFiring(t *testing.T) {
	out, err := Projector{}.Project(firingEvent(t))
	require.NoError(t, err)
	require.Len(t, out, 1)

	n := out[0]
	require.Equal(t, notify.SourceAlerts, n.Source)
	require.Equal(t, notify.EventAlertFiring, n.Type)
	// Broadcast: addressed to whoever watches the chat, nobody in particular.
	require.Equal(t, notify.Actor{}, n.Recipient)
	// The dedup key rides the alert's id, so a repeat_interval resend of the
	// same firing does not announce twice.
	require.Equal(t, "alertmanager:abc:alert.firing:2026-05-01T10:00:00Z", n.EventID)
	require.Contains(t, n.Body, "error ratio 12%")
	require.Contains(t, n.Body, "severity=critical service=checkout")
	require.NotContains(t, n.Body, "pod=", "the full label set belongs in Alertmanager, not a chat")
	require.Contains(t, n.Body, "https://runbooks.example.com/high-error-rate")
}

func TestProjectResolved(t *testing.T) {
	e := firingEvent(t)
	e.Type = event.TypeAlertResolved
	out, err := Projector{}.Project(e)
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Equal(t, notify.EventAlertResolved, out[0].Type)
}

// A projector subscribed to a source is handed everything that source emits.
func TestProjectIgnoresOtherTypes(t *testing.T) {
	e := firingEvent(t)
	e.Type = event.TypeMRUpdated
	out, err := Projector{}.Project(e)
	require.NoError(t, err)
	require.Empty(t, out)
}

func TestRenderFiringAndResolved(t *testing.T) {
	firing, err := notify.DefaultRenderer{}.Render(notify.Event{
		Source: notify.SourceAlerts, Type: notify.EventAlertFiring,
		Title: "HighErrorRate", URL: "https://prometheus.example.com/graph", Body: "error ratio 12%",
	})
	require.NoError(t, err)
	require.Contains(t, firing, "Firing:")
	require.Contains(t, firing, "[HighErrorRate](https://prometheus.example.com/graph)")
	require.Contains(t, firing, "error ratio 12%")

	resolved, err := notify.DefaultRenderer{}.Render(notify.Event{
		Source: notify.SourceAlerts, Type: notify.EventAlertResolved, Title: "HighErrorRate",
	})
	require.NoError(t, err)
	require.Contains(t, resolved, "Resolved:")
}
