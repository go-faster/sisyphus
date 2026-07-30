// Package jira is the Jira half of the notification gateway: a Projector that
// turns canonical internal/event Events — emitted by the Jira source adapter
// in internal/ingest/jira — into per-recipient notify.Events. It does not
// fetch anything; the source adapter polls Jira exactly once and the router
// delivers the same event here and to the knowledge-graph ingest.
package jira

import (
	"fmt"

	"github.com/go-faster/errors"

	"github.com/go-faster/sisyphus/internal/event"
	ingestjira "github.com/go-faster/sisyphus/internal/ingest/jira"
	"github.com/go-faster/sisyphus/internal/notify"
)

// Projector implements notify.Projector for Jira: an event.TypeIssueUpdated
// event with an assignee becomes one EventIssueAssigned notify.Event; an
// unassigned issue projects to nothing. The EventID matches the pre-spine
// collector's exactly, so existing outbox dedup keys still suppress
// already-delivered notifications.
type Projector struct{}

func (Projector) Project(e event.Event) ([]notify.Event, error) {
	var p ingestjira.IssuePayload
	if err := e.DecodePayload(&p); err != nil {
		return nil, errors.Wrap(err, "decode issue payload")
	}
	if p.AssigneeAccountID == "" {
		return nil, nil
	}

	return []notify.Event{{
		Source: notify.SourceJira,
		Type:   notify.EventIssueAssigned,
		Recipient: notify.Actor{
			Source:  notify.SourceJira,
			Key:     p.AssigneeAccountID,
			Display: p.AssigneeDisplay,
		},
		Actor:      notify.Actor{Source: notify.SourceJira, Display: e.Actor.Display},
		Title:      e.Subject.Title,
		URL:        e.Subject.URL,
		ObjectID:   e.Subject.ID,
		EventID:    fmt.Sprintf("jira_assign:%s:%s", e.Subject.ID, p.AssigneeAccountID),
		OccurredAt: e.OccurredAt,
	}}, nil
}
