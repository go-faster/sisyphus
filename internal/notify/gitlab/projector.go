// Package gitlab is the GitLab half of the notification gateway: a Projector
// that fans canonical internal/event Events — emitted by the GitLab source
// adapter in internal/ingest/gitlab — into per-recipient notify.Events. It
// does not fetch anything; the source adapter polls GitLab exactly once and
// the router delivers the same event here and to the knowledge-graph ingest.
package gitlab

import (
	"fmt"

	"github.com/go-faster/errors"

	"github.com/go-faster/sisyphus/internal/event"
	ingestgitlab "github.com/go-faster/sisyphus/internal/ingest/gitlab"
	"github.com/go-faster/sisyphus/internal/notify"
)

// Projector implements notify.Projector for GitLab: it fans an
// event.TypeMRUpdated event out into one notify.Event per current assignee
// (EventMRAssigned) and per current reviewer (EventMRReviewRequested). The
// EventID strings match the pre-spine collector's exactly, so existing outbox
// dedup keys still suppress already-delivered notifications.
type Projector struct{}

func (Projector) Project(e event.Event) ([]notify.Event, error) {
	var p ingestgitlab.MRPayload
	if err := e.DecodePayload(&p); err != nil {
		return nil, errors.Wrap(err, "decode mr payload")
	}

	actor := notify.Actor{
		Source:  notify.SourceGitLab,
		Key:     e.Actor.Key,
		Display: e.Actor.Display,
		URL:     e.Actor.URL,
	}
	objectID := e.Subject.ID
	// The MR itself is the only link a GitLab event carries, and it is the
	// one the recipient is being asked to act on.
	buttons := []notify.Button{{Text: "Open merge request", URL: e.Subject.URL}}

	var out []notify.Event
	for _, username := range p.Assignees {
		out = append(out, notify.Event{
			Source:     notify.SourceGitLab,
			Type:       notify.EventMRAssigned,
			Recipient:  notify.Actor{Source: notify.SourceGitLab, Key: username},
			Actor:      actor,
			Title:      e.Subject.Title,
			Buttons:    buttons,
			URL:        e.Subject.URL,
			ObjectID:   objectID,
			EventID:    fmt.Sprintf("gitlab_mr_assign:%s:%s", objectID, username),
			OccurredAt: e.OccurredAt,
		})
	}
	for _, username := range p.Reviewers {
		out = append(out, notify.Event{
			Source:     notify.SourceGitLab,
			Type:       notify.EventMRReviewRequested,
			Recipient:  notify.Actor{Source: notify.SourceGitLab, Key: username},
			Actor:      actor,
			Title:      e.Subject.Title,
			Buttons:    buttons,
			URL:        e.Subject.URL,
			ObjectID:   objectID,
			EventID:    fmt.Sprintf("gitlab_mr_review:%s:%s", objectID, username),
			OccurredAt: e.OccurredAt,
		})
	}
	return out, nil
}
