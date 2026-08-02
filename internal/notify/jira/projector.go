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
// It also projects the issue's comments into EventIssueCommented and
// EventIssueMentioned (see notify.CommentRule): assignment says work arrived,
// comments say the conversation about it moved.
//
// Staleness drops assignments and comments the payload proves are old: the
// event states the issue's current assignee and current comments, so any edit
// to a long-assigned issue would otherwise announce that assignment as if it
// just happened, and the first poll after comment events shipped would
// announce every comment in the fetched window.
type Projector struct {
	Staleness notify.Staleness
}

func (pr Projector) Project(e event.Event) ([]notify.Event, error) {
	var p ingestjira.IssuePayload
	if err := e.DecodePayload(ingestjira.IssuePayloadVersion, &p); err != nil {
		return nil, errors.Wrap(err, "decode issue payload")
	}

	assignee := notify.Actor{
		Source:  notify.SourceJira,
		Key:     p.AssigneeAccountID,
		Display: p.AssigneeDisplay,
	}

	var out []notify.Event
	if assignee.Key != "" && pr.Staleness.Fresh(p.AssignedAt) {
		out = append(out, notify.Event{
			Source:    notify.SourceJira,
			Type:      notify.EventIssueAssigned,
			Recipient: assignee,
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
		})
	}

	// Comments go to the *current* assignee, not the stale-filtered one above:
	// a comment on an issue assigned to you months ago is still news, even
	// though the assignment itself is not. An unassigned issue still projects
	// its mentions — being named does not require being assigned.
	var watchers []notify.Actor
	if assignee.Key != "" {
		watchers = append(watchers, assignee)
	}
	rule := notify.CommentRule{
		Source:     notify.SourceJira,
		Commented:  notify.EventIssueCommented,
		Mentioned:  notify.EventIssueMentioned,
		ButtonText: "Open comment",
		Staleness:  pr.Staleness,
	}
	subject := notify.CommentSubject{ObjectID: e.Subject.ID, Title: e.Subject.Title, URL: e.Subject.URL}
	out = append(out, rule.Project(subject, watchers, comments(p.Comments))...)

	return out, nil
}

// comments maps the payload's comments onto the shared rule's shape.
func comments(in []ingestjira.Comment) []notify.Comment {
	out := make([]notify.Comment, 0, len(in))
	for _, c := range in {
		out = append(out, notify.Comment{
			ID: c.ID,
			Author: notify.Actor{
				Source:  notify.SourceJira,
				Key:     c.Author.ID,
				Display: c.Author.Display,
				URL:     c.Author.URL,
			},
			Body:      c.Body,
			Mentions:  c.Mentions,
			URL:       c.URL,
			CreatedAt: c.CreatedAt,
		})
	}
	return out
}
