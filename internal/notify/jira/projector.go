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
//
// The actor comes from the payload's assigner, not from the envelope's
// [event.Actor]: the notification reads "X assigned you this", and the
// envelope names whoever touched the issue last, who may have only edited a
// label. An unknown assigner stays zero — the renderer says "Someone" — since
// naming the wrong colleague is worse than naming none. Filling Actor.Key
// from the same identity space as the recipient's is also what lets
// notify.Event.SelfCaused suppress your own assignments.
//
// Staleness drops assignments the payload proves are old: the event states the
// issue's current assignee, so any edit to a long-assigned issue would
// otherwise announce that assignment as if it just happened.
type Projector struct {
	Staleness notify.Staleness
}

func (pr Projector) Project(e event.Event) ([]notify.Event, error) {
	var p ingestjira.IssuePayload
	if err := e.DecodePayload(&p); err != nil {
		return nil, errors.Wrap(err, "decode issue payload")
	}
	if p.AssigneeAccountID == "" {
		return nil, nil
	}
	if !pr.Staleness.Fresh(p.AssignedAt) {
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
		Actor: notify.Actor{
			Source:  notify.SourceJira,
			Key:     p.AssignedBy.ID,
			Display: p.AssignedBy.Display,
			URL:     p.AssignedBy.URL,
		},
		Title:      e.Subject.Title,
		Buttons:    []notify.Button{{Text: "Open issue", URL: e.Subject.URL}},
		URL:        e.Subject.URL,
		ObjectID:   e.Subject.ID,
		EventID:    fmt.Sprintf("jira_assign:%s:%s", e.Subject.ID, p.AssigneeAccountID),
		OccurredAt: e.OccurredAt,
	}}, nil
}
