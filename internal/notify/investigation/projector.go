// Package investigation is the alerting half of the notification gateway: a
// Projector turning an agent's finished investigation into the broadcast
// notification a team chat receives.
//
// Unlike the GitLab and Jira projectors it names no recipient. An
// investigation is addressed to whoever watches the alert chat, so the
// Broadcaster resolves the target from deployment config rather than from a
// user's linked identity.
package investigation

import (
	"strings"

	"github.com/go-faster/errors"

	"github.com/go-faster/sisyphus/internal/agent"
	"github.com/go-faster/sisyphus/internal/event"
	"github.com/go-faster/sisyphus/internal/notify"
)

// maxFindingsChars bounds the findings text pasted into a chat message.
// Telegram's own limit is far higher, but a wall of LLM output in an alert
// channel is not a notification — the full report stays in the job row.
const maxFindingsChars = 700

// Projector implements notify.Projector for finished investigations.
type Projector struct{}

func (Projector) Project(e event.Event) ([]notify.Event, error) {
	var p agent.ReportPayload
	if err := e.DecodePayload(&p); err != nil {
		return nil, errors.Wrap(err, "decode report payload")
	}

	return []notify.Event{{
		Source:     notify.SourceAlerts,
		Type:       notify.EventInvestigationCompleted,
		Title:      e.Subject.Title,
		Body:       body(p),
		URL:        e.Subject.URL,
		ObjectID:   e.Subject.ID,
		EventID:    e.ID,
		OccurredAt: e.OccurredAt,
	}}, nil
}

// body renders the verdict, the findings and any suggested actions.
func body(p agent.ReportPayload) string {
	var b strings.Builder
	if p.Verdict != "" {
		b.WriteString("Verdict: ")
		b.WriteString(string(p.Verdict))
		b.WriteString("\n")
	}
	if f := strings.TrimSpace(p.Findings); f != "" {
		b.WriteString(truncate(f, maxFindingsChars))
		b.WriteString("\n")
	}
	for _, action := range p.Actions {
		if a := strings.TrimSpace(action); a != "" {
			b.WriteString("- ")
			b.WriteString(a)
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func truncate(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return strings.TrimSpace(string(runes[:limit])) + "…"
}
