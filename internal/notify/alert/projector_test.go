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
	require.Equal(t, "error ratio 12%", n.Description)
	require.Equal(t, []notify.Label{
		{Key: "severity", Value: "critical"},
		{Key: "service", Value: "checkout"},
	}, n.Labels, "the full label set belongs in Alertmanager, not a chat")
	// The runbook is a button, not a bare URL pasted into the text.
	require.Equal(t, []notify.Button{
		{Text: "Runbook", URL: "https://runbooks.example.com/high-error-rate"},
	}, n.Buttons)
}

func TestProjectButtons(t *testing.T) {
	e := firingEvent(t)
	e, err := e.WithPayload(ingestalert.AlertPayload{
		Annotations: map[string]string{
			"runbook_url":   "https://runbooks.example.com/high-error-rate",
			"dashboard_url": "https://grafana.example.com/d/abc",
		},
		ExternalURL: "https://alertmanager.example.com",
	})
	require.NoError(t, err)

	out, err := Projector{}.Project(e)
	require.NoError(t, err)
	require.Equal(t, []notify.Button{
		{Text: "Runbook", URL: "https://runbooks.example.com/high-error-rate"},
		{Text: "Dashboard", URL: "https://grafana.example.com/d/abc"},
		{Text: "Alertmanager", URL: "https://alertmanager.example.com"},
	}, out[0].Buttons)
}

// Only annotations become buttons: an alert's labels and description carry
// whatever the alerting target reported, and a URL in there is not a link
// anyone vetted.
func TestProjectButtonsIgnoreLabelsAndDescription(t *testing.T) {
	e := firingEvent(t)
	e, err := e.WithPayload(ingestalert.AlertPayload{
		Labels:      map[string]string{"instance": "https://evil.example.com"},
		Annotations: map[string]string{"description": "see https://evil.example.com for details"},
	})
	require.NoError(t, err)

	out, err := Projector{}.Project(e)
	require.NoError(t, err)
	require.Empty(t, out[0].Buttons)
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

// The rendered alert is the shape an on-call reader sees: transition and
// name on one line, description and labels below.
func TestRenderFiringAndResolved(t *testing.T) {
	out, err := Projector{}.Project(firingEvent(t))
	require.NoError(t, err)

	firing, err := notify.DefaultRenderer{}.Render(out[0])
	require.NoError(t, err)
	// The backslashes are CommonMark escapes of the alert's own punctuation:
	// ingested text must not bleed formatting, and the Telegram renderer
	// resolves them back to the literal characters.
	require.Equal(t,
		"🔥 _Firing:_ **[HighErrorRate\\: 5xx above 5\\%](https://prometheus.example.com/graph)**\n\n"+
			"error ratio 12\\%"+notify.LineBreak+
			"`severity=critical service=checkout`",
		firing)

	e := firingEvent(t)
	e.Type = event.TypeAlertResolved
	out, err = Projector{}.Project(e)
	require.NoError(t, err)
	resolved, err := notify.DefaultRenderer{}.Render(out[0])
	require.NoError(t, err)
	require.Contains(t, resolved, "✅ _Resolved:_")
}

// A bare newline is a CommonMark soft break, which the Telegram renderer turns
// into a space — that collapsed the whole alert onto one line.
func TestRenderUsesHardLineBreaks(t *testing.T) {
	out, err := Projector{}.Project(firingEvent(t))
	require.NoError(t, err)

	text, err := notify.DefaultRenderer{}.Render(out[0])
	require.NoError(t, err)
	require.Contains(t, text, notify.LineBreak)
}
