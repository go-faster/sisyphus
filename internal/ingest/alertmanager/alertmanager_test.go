package alertmanager

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/event"
)

const firingBody = `{
  "version": "4",
  "groupKey": "{}:{alertname=\"HighErrorRate\"}",
  "receiver": "sisyphus",
  "status": "firing",
  "externalURL": "https://alertmanager.example.com",
  "alerts": [
    {
      "status": "firing",
      "labels": {"alertname": "HighErrorRate", "severity": "critical", "service": "checkout"},
      "annotations": {"summary": "5xx rate above 5%"},
      "startsAt": "2026-05-01T10:00:00Z",
      "endsAt": "0001-01-01T00:00:00Z",
      "generatorURL": "https://prometheus.example.com/graph?g0.expr=up",
      "fingerprint": "abc123"
    }
  ]
}`

func TestEventsFromWebhookFiring(t *testing.T) {
	events, err := EventsFromWebhook([]byte(firingBody))
	require.NoError(t, err)
	require.Len(t, events, 1)

	e := events[0]
	require.Equal(t, event.SourceAlertmanager, e.Source)
	require.Equal(t, event.TypeAlertFiring, e.Type)
	require.Equal(t, event.SeverityCritical, e.Severity)
	require.Equal(t, "alertmanager:abc123:alert.firing:2026-05-01T10:00:00Z", e.ID)
	require.Equal(t, "abc123", e.Subject.ID)
	require.Equal(t, "HighErrorRate: 5xx rate above 5%", e.Subject.Title)
	require.Equal(t, "https://prometheus.example.com/graph?g0.expr=up", e.Subject.URL)
	require.Equal(t, time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC), e.OccurredAt)
	require.Equal(t, "checkout", e.Attr("service"))
	require.Equal(t, "sisyphus", e.Attr("receiver"))

	var p AlertPayload
	require.NoError(t, e.DecodePayload(AlertPayloadVersion, &p))
	require.Equal(t, "5xx rate above 5%", p.Annotations["summary"])
	require.Equal(t, "https://alertmanager.example.com", p.ExternalURL)
}

// A resend of the same firing alert must carry the same ID (destinations
// dedup on it), while the resolve is a different occurrence of the same
// subject.
func TestEventsFromWebhookResolvedIsDistinct(t *testing.T) {
	firing, err := EventsFromWebhook([]byte(firingBody))
	require.NoError(t, err)

	resend, err := EventsFromWebhook([]byte(firingBody))
	require.NoError(t, err)
	require.Equal(t, firing[0].ID, resend[0].ID)

	resolvedBody := `{"version":"4","alerts":[{"status":"resolved",
	  "labels":{"alertname":"HighErrorRate","severity":"critical"},
	  "startsAt":"2026-05-01T10:00:00Z","endsAt":"2026-05-01T10:30:00Z",
	  "fingerprint":"abc123"}]}`
	resolved, err := EventsFromWebhook([]byte(resolvedBody))
	require.NoError(t, err)
	require.Len(t, resolved, 1)
	require.Equal(t, event.TypeAlertResolved, resolved[0].Type)
	require.Equal(t, firing[0].Subject.ID, resolved[0].Subject.ID)
	require.NotEqual(t, firing[0].ID, resolved[0].ID)
	require.Equal(t, time.Date(2026, 5, 1, 10, 30, 0, 0, time.UTC), resolved[0].OccurredAt)
}

func TestSeverityFromLabels(t *testing.T) {
	for _, tt := range []struct {
		label string
		want  event.Severity
	}{
		{"critical", event.SeverityCritical},
		{"PAGE", event.SeverityCritical},
		{"warning", event.SeverityWarning},
		{"minor", event.SeverityWarning},
		{"info", event.SeverityInfo},
		{"", ""},
		{"weird", ""},
	} {
		t.Run(tt.label, func(t *testing.T) {
			require.Equal(t, tt.want, severityFromLabels(map[string]string{labelSeverity: tt.label}))
		})
	}
}

// Without a fingerprint the label set has to identify the alert, stably and
// independent of map iteration order.
func TestEventsFromWebhookWithoutFingerprint(t *testing.T) {
	body := `{"version":"4","alerts":[{"status":"firing",
	  "labels":{"alertname":"NoFingerprint","zone":"eu","severity":"warning"},
	  "startsAt":"2026-05-01T10:00:00Z"}]}`

	first, err := EventsFromWebhook([]byte(body))
	require.NoError(t, err)
	second, err := EventsFromWebhook([]byte(body))
	require.NoError(t, err)

	require.Equal(t, "alertname=NoFingerprint,severity=warning,zone=eu", first[0].Subject.ID)
	require.Equal(t, first[0].ID, second[0].ID)
}

func TestEventsFromWebhookInvalid(t *testing.T) {
	_, err := EventsFromWebhook([]byte("not json"))
	require.Error(t, err)
}
