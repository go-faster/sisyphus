package investigation

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/agent"
	"github.com/go-faster/sisyphus/internal/event"
)

func reportEvent(t *testing.T, report agent.Report) event.Event {
	t.Helper()
	trigger := event.Event{
		ID:       "alertmanager:abc:alert.firing:2026-05-01T10:00:00Z",
		Source:   event.SourceAlertmanager,
		Type:     event.TypeAlertFiring,
		Severity: event.SeverityCritical,
		Subject:  event.Ref{ID: "abc", Title: "HighErrorRate: 5xx above 5%", URL: "https://prometheus.example.com/graph"},
	}
	e, err := agent.EventFromReport(uuid.MustParse("11111111-2222-3333-4444-555555555555"), trigger, report,
		time.Date(2026, 5, 1, 10, 5, 0, 0, time.UTC))
	require.NoError(t, err)
	return e
}

func TestProject(t *testing.T) {
	e := reportEvent(t, agent.Report{
		Verdict:  agent.VerdictKnownIssue,
		Problem:  "checkout 5xx",
		Findings: "upstream payment gateway is timing out",
		Actions:  []string{"page the payments team", ""},
	})

	out, err := Projector{}.Project(e)
	require.NoError(t, err)
	require.Len(t, out, 1)

	n := out[0]
	require.Equal(t, "HighErrorRate: 5xx above 5%", n.Title, "named by what triggered it, not by the agent's restatement")
	require.Equal(t, "https://prometheus.example.com/graph", n.URL)
	require.Equal(t, e.ID, n.EventID)
	require.Contains(t, n.Body, string(agent.VerdictKnownIssue))
	require.Contains(t, n.Body, "upstream payment gateway is timing out")
	require.Contains(t, n.Body, "- page the payments team")
	require.NotContains(t, n.Body, "- \n", "blank actions are dropped")

	// No recipient: a broadcast is addressed by the Broadcaster's targets.
	require.Zero(t, n.Recipient)
}

func TestProjectTruncatesFindings(t *testing.T) {
	e := reportEvent(t, agent.Report{
		Verdict:  agent.VerdictNeedsInvestigation,
		Findings: strings.Repeat("x", maxFindingsChars+200),
	})

	out, err := Projector{}.Project(e)
	require.NoError(t, err)
	require.Contains(t, out[0].Body, "…")
	require.Less(t, len([]rune(out[0].Body)), maxFindingsChars+100)
}

func TestProjectBadPayload(t *testing.T) {
	_, err := Projector{}.Project(event.Event{Payload: []byte("not json")})
	require.Error(t, err)
}
