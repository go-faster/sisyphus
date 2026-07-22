package jira

import (
	"fmt"

	"github.com/go-faster/errors"

	"github.com/go-faster/sisyphus/internal/event"
	"github.com/go-faster/sisyphus/internal/notify"
)

// Projector implements notify.Projector for Jira: an event.TypeIssueUpdated
// event with an assignee becomes one EventIssueAssigned notify.Event; an
// unassigned issue projects to nothing. The EventID matches the pre-spine
// collector's exactly, so existing outbox dedup keys still suppress
// already-delivered notifications.
type Projector struct{}

func (Projector) Project(e event.Event) ([]notify.Event, error) {
	var p IssuePayload
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
