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
// EventMRMentioned — see commentRule), the EventMRThreadResolved events its
// resolved threads produce (see projectResolutions), and the outcome events
// addressed to its author and assignees: EventMRApproved per standing
// approval, EventMRMerged once it lands. It also handles the separate
// event.TypeMRConflict events the conflict sweep emits (see projectConflict). The assignment EventID strings match the pre-spine
// collector's exactly, so existing outbox dedup keys still suppress
// already-delivered notifications.
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
	// A conflict carries a different payload and a different question ("this
	// stopped being mergeable"), so it branches off before the MR payload is
	// decoded at all.
	if e.Type == event.TypeMRConflict {
		return pr.projectConflict(e)
	}

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
		for _, username := range outcomeRecipients(p) {
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

	// An approval is standing state, present in every payload from the moment
	// it is given, so staleness on the approval's own timestamp is what tells
	// one just given from one standing for weeks. Its dedup id is keyed on the
	// approver rather than on a timestamp: a re-approval after an unapproval
	// is the same approver saying the same thing, and the recipient does not
	// need telling twice.
	for _, a := range p.Approvals {
		if a.User.Username == "" || !pr.Staleness.Fresh(a.At) {
			continue
		}
		approver := notify.Actor{
			Source:  notify.SourceGitLab,
			Key:     a.User.Username,
			Display: a.User.Display,
			URL:     a.User.URL,
		}
		for _, username := range outcomeRecipients(p) {
			out = append(out, notify.Event{
				Source:     notify.SourceGitLab,
				Type:       notify.EventMRApproved,
				Recipient:  notify.Actor{Source: notify.SourceGitLab, Key: username},
				Actor:      approver,
				Title:      e.Subject.Title,
				Buttons:    buttons,
				URL:        e.Subject.URL,
				ObjectID:   objectID,
				EventID:    fmt.Sprintf("gitlab_mr_approved:%s:%s:%s", objectID, a.User.Username, username),
				OccurredAt: a.At,
			})
		}
	}

	// Comments go to the MR's *current* members, not the stale-filtered sets
	// above: a comment on an MR assigned to you months ago is still news, even
	// though the assignment itself is not.
	//
	// The author is a watcher too, and first, because they are the one person
	// guaranteed to care: an MR opened without assigning anyone — the common
	// shape for a small change — otherwise has no watchers at all, so a
	// colleague's review comment on it notifies nobody. Their own comments
	// still never notify them (CommentRule skips a watcher's own), and being
	// author as well as assignee is deduped to one message.
	watchers := make([]notify.Actor, 0, 1+len(p.Assignees)+len(p.Reviewers))
	if p.Author.Username != "" {
		watchers = append(watchers, notify.Actor{Source: notify.SourceGitLab, Key: p.Author.Username})
	}
	for _, username := range slices.Concat(p.Assignees, p.Reviewers) {
		watchers = append(watchers, notify.Actor{Source: notify.SourceGitLab, Key: username})
	}
	subject := notify.CommentSubject{ObjectID: objectID, Title: e.Subject.Title, URL: e.Subject.URL}
	out = append(out, pr.commentRule().Project(subject, watchers, comments(p.Comments))...)
	out = append(out, pr.projectResolutions(e, p)...)

	return out, nil
}

// outcomeRecipients are the people an MR's outcome — approved, merged — is
// addressed to: the author first, since what happens to an MR is news to
// whoever wrote it before it is news to anyone else, then its assignees.
// Deduped, so an author assigned to their own MR is told once.
//
// Reviewers are deliberately absent. They are told when their review is
// requested and when the conversation moves; being told that a colleague also
// approved, on every MR they review, is the noise that makes people mute the
// whole thing.
func outcomeRecipients(p ingestgitlab.MRPayload) []string {
	out := make([]string, 0, 1+len(p.Assignees))
	if p.Author.Username != "" {
		out = append(out, p.Author.Username)
	}
	for _, username := range p.Assignees {
		if username != "" && !slices.Contains(out, username) {
			out = append(out, username)
		}
	}
	return out
}

// comments maps the payload's comments onto the shared rule's shape.
func comments(in []ingestgitlab.Comment) []notify.Comment {
	out := make([]notify.Comment, 0, len(in))
	for _, c := range in {
		out = append(out, notify.Comment{
			ID:       c.ID,
			ThreadID: c.ThreadID,
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
