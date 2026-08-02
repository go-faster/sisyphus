package agent

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/event"
	"github.com/go-faster/sisyphus/internal/index"
)

func TestEventFromReport(t *testing.T) {
	jobID := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	trigger := event.Event{
		ID:         "alertmanager:abc:alert.firing:2026-05-01T10:00:00Z",
		Source:     event.SourceAlertmanager,
		Type:       event.TypeAlertFiring,
		Severity:   event.SeverityCritical,
		Subject:    event.Ref{ID: "abc", Title: "HighErrorRate", URL: "https://prometheus.example.com/graph"},
		Attributes: map[string]string{"service": "checkout"},
	}
	report := Report{
		Verdict:  VerdictSolved,
		Problem:  "checkout 5xx",
		Findings: "bad deploy rolled back",
		Links:    []index.Link{{Text: "dashboard", URL: "https://grafana.example.com/d/1"}},
	}
	completedAt := time.Date(2026, 5, 1, 10, 5, 0, 0, time.UTC)

	e, err := EventFromReport(jobID, trigger, report, completedAt)
	require.NoError(t, err)

	require.Equal(t, event.SourceAgent, e.Source)
	require.Equal(t, event.TypeInvestigationCompleted, e.Type)
	require.Equal(t, "investigation:"+jobID.String(), e.ID)
	require.Equal(t, "HighErrorRate", e.Subject.Title, "named by the trigger, not the agent's restatement")
	require.Equal(t, trigger.Subject.URL, e.Subject.URL)
	require.Equal(t, event.SeverityCritical, e.Severity, "a report on a critical alert is itself critical")
	require.Equal(t, completedAt, e.OccurredAt)
	require.Equal(t, "checkout", e.Attr("service"))

	var p ReportPayload
	require.NoError(t, e.DecodePayload(ReportPayloadVersion, &p))
	require.Equal(t, jobID, p.JobID)
	require.Equal(t, trigger.ID, p.TriggerEventID, "the report points back at the alert that caused it")
	require.Equal(t, VerdictSolved, p.Verdict)
	require.Len(t, p.Links, 1)
}

// The ID is the job's, not the finish time's, so re-delivering a completed
// investigation stays one occurrence.
func TestEventFromReportIDIsPerJob(t *testing.T) {
	jobID := uuid.New()
	first, err := EventFromReport(jobID, event.Event{}, Report{}, time.Unix(0, 0))
	require.NoError(t, err)
	second, err := EventFromReport(jobID, event.Event{}, Report{}, time.Unix(500, 0))
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID)
}

func TestEventFromReportTitleFallback(t *testing.T) {
	e, err := EventFromReport(uuid.New(), event.Event{}, Report{Problem: "disk filling up"}, time.Unix(0, 0))
	require.NoError(t, err)
	require.Equal(t, "disk filling up", e.Subject.Title)

	e, err = EventFromReport(uuid.New(), event.Event{}, Report{}, time.Unix(0, 0))
	require.NoError(t, err)
	require.Equal(t, "investigation", e.Subject.Title)
}
