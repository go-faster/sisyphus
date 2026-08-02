// Package gitlab is the GitLab half of the notification gateway: a Projector
// that fans canonical internal/event Events — emitted by the GitLab source
// adapter in internal/ingest/gitlab — into per-recipient notify.Events. It
// does not fetch anything; the source adapter polls GitLab exactly once and
// the router delivers the same event here and to the knowledge-graph ingest.
package gitlab

import (
	"fmt"
	"slices"

	"github.com/go-faster/errors"

	"github.com/go-faster/sisyphus/internal/event"
	ingestgitlab "github.com/go-faster/sisyphus/internal/ingest/gitlab"
	"github.com/go-faster/sisyphus/internal/notify"
)

// Projector implements notify.Projector for GitLab: it fans an
// event.TypeMRUpdated event out into one notify.Event per current assignee
// (EventMRAssigned) and per current reviewer (EventMRReviewRequested), the
// conversation events its comments produce (EventMRCommented,
// EventMRMentioned — see commentRule), and, once it lands, EventMRMerged to
// its author and assignees. The assignment EventID strings match
// the pre-spine collector's exactly, so existing outbox dedup keys still
// suppress already-delivered notifications.
//
// Staleness drops membership changes and comments the payload proves are old:
// the event states the MR's current members and current comments, so any push
// to a long-assigned MR would otherwise announce that assignment as if it just
// happened, and the first poll after comment events shipped would announce
// every comment in the fetched window.
type Projector struct {
	Staleness notify.Staleness
}

// mergedState is the value GitLab reports in an MR's state field once it has
// been merged.
const mergedState = "merged"

// commentRule is the shared comment projection, named for GitLab.
func (pr Projector) commentRule() notify.CommentRule {
	return notify.CommentRule{
		Source:     notify.SourceGitLab,
		Commented:  notify.EventMRCommented,
		Mentioned:  notify.EventMRMentioned,
		ButtonText: "Open comment",
		Staleness:  pr.Staleness,
	}
}

func (pr Projector) Project(e event.Event) ([]notify.Event, error) {
	var p ingestgitlab.MRPayload
	if err := e.DecodePayload(ingestgitlab.MRPayloadVersion, &p); err != nil {
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

	// A merge is terminal and happens once, so unlike a membership change it
	// needs no timestamp in its dedup id — an MR cannot be merged twice.
	// Staleness still gates it, because the event states "merged" on every
	// poll from the merge onwards and the first run after this shipped would
	// otherwise announce every MR merged in the fetched window.
	if p.State == mergedState && pr.Staleness.Fresh(p.MergedAt) {
		merger := notify.Actor{
			Source:  notify.SourceGitLab,
			Key:     p.MergedBy.Username,
			Display: p.MergedBy.Display,
			URL:     p.MergedBy.URL,
		}
		// The author first: an MR landing is news to whoever wrote it before
		// it is news to anyone else. Assignees follow, deduped so an author
		// assigned to their own MR is told once.
		recipients := make([]string, 0, 1+len(p.Assignees))
		if p.Author.Username != "" {
			recipients = append(recipients, p.Author.Username)
		}
		for _, username := range p.Assignees {
			if !slices.Contains(recipients, username) {
				recipients = append(recipients, username)
			}
		}
		for _, username := range recipients {
			out = append(out, notify.Event{
				Source:     notify.SourceGitLab,
				Type:       notify.EventMRMerged,
				Recipient:  notify.Actor{Source: notify.SourceGitLab, Key: username},
				Actor:      merger,
				Title:      e.Subject.Title,
				Buttons:    buttons,
				URL:        e.Subject.URL,
				ObjectID:   objectID,
				EventID:    fmt.Sprintf("gitlab_mr_merged:%s:%s", objectID, username),
				OccurredAt: p.MergedAt,
			})
		}
	}

	// Comments go to the MR's *current* members, not the stale-filtered sets
	// above: a comment on an MR assigned to you months ago is still news, even
	// though the assignment itself is not.
	watchers := make([]notify.Actor, 0, len(p.Assignees)+len(p.Reviewers))
	for _, username := range slices.Concat(p.Assignees, p.Reviewers) {
		watchers = append(watchers, notify.Actor{Source: notify.SourceGitLab, Key: username})
	}
	subject := notify.CommentSubject{ObjectID: objectID, Title: e.Subject.Title, URL: e.Subject.URL}
	out = append(out, pr.commentRule().Project(subject, watchers, comments(p.Comments))...)

	return out, nil
}

// comments maps the payload's comments onto the shared rule's shape.
func comments(in []ingestgitlab.Comment) []notify.Comment {
	out := make([]notify.Comment, 0, len(in))
	for _, c := range in {
		out = append(out, notify.Comment{
			ID: c.ID,
			Author: notify.Actor{
				Source:  notify.SourceGitLab,
				Key:     c.Author.Username,
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
