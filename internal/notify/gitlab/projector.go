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
//
// Staleness drops membership changes the payload proves are old: the event
// states the MR's current members, so any push to a long-assigned MR would
// otherwise announce that assignment as if it just happened.
type Projector struct {
	Staleness notify.Staleness
}

func (pr Projector) Project(e event.Event) ([]notify.Event, error) {
	var p ingestgitlab.MRPayload
	if err := e.DecodePayload(&p); err != nil {
		return nil, errors.Wrap(err, "decode mr payload")
	}

	// Each notification names the person behind that specific membership
	// change, not the envelope's actor — that is whoever touched the MR last,
	// who may have only pushed a commit. An unknown one stays zero and the
	// renderer says "Someone": naming the wrong colleague is worse than
	// naming none.
	assigner := notify.Actor{
		Source:  notify.SourceGitLab,
		Key:     p.AssignedBy.Username,
		Display: p.AssignedBy.Display,
		URL:     p.AssignedBy.URL,
	}
	reviewRequester := notify.Actor{
		Source:  notify.SourceGitLab,
		Key:     p.ReviewRequestedBy.Username,
		Display: p.ReviewRequestedBy.Display,
		URL:     p.ReviewRequestedBy.URL,
	}
	objectID := e.Subject.ID
	// The MR itself is the only link a GitLab event carries, and it is the
	// one the recipient is being asked to act on.
	buttons := []notify.Button{{Text: "Open merge request", URL: e.Subject.URL}}

	// A membership change the payload proves is old notifies nobody: the
	// event states current members, so it would otherwise re-announce an
	// assignment made months ago the next time anyone pushes.
	assignees, reviewers := p.Assignees, p.Reviewers
	if !pr.Staleness.Fresh(p.AssignedAt) {
		assignees = nil
	}
	if !pr.Staleness.Fresh(p.ReviewRequestedAt) {
		reviewers = nil
	}

	var out []notify.Event
	for _, username := range assignees {
		out = append(out, notify.Event{
			Source:     notify.SourceGitLab,
			Type:       notify.EventMRAssigned,
			Recipient:  notify.Actor{Source: notify.SourceGitLab, Key: username},
			Actor:      assigner,
			Title:      e.Subject.Title,
			Buttons:    buttons,
			URL:        e.Subject.URL,
			ObjectID:   objectID,
			EventID:    fmt.Sprintf("gitlab_mr_assign:%s:%s", objectID, username),
			OccurredAt: e.OccurredAt,
		})
	}
	for _, username := range reviewers {
		out = append(out, notify.Event{
			Source:     notify.SourceGitLab,
			Type:       notify.EventMRReviewRequested,
			Recipient:  notify.Actor{Source: notify.SourceGitLab, Key: username},
			Actor:      reviewRequester,
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
