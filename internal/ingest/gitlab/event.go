package gitlab

import (
	"fmt"
	"time"

	"github.com/go-faster/errors"

	chunkgitlab "github.com/go-faster/sisyphus/internal/chunk/gitlab"
	"github.com/go-faster/sisyphus/internal/event"
)

// MRPayloadVersion is [MRPayload]'s schema version: it is stamped onto every
// event this adapter emits and required by every reader of one. Bump it when a
// field changes type or meaning. A purely additive field needs no bump — an
// older writer's payload still decodes correctly, with the new field zero.
const MRPayloadVersion = 1

// MRPayload is the source-typed body of an [event.TypeMRUpdated] event: the
// merge request's current member sets, who last put someone in them, its
// newest comments, and whether it has been merged. Only
// a destination that understands GitLab decodes it — today the notification
// gateway's projector.
//
// The assigner and the review requester ride here rather than in
// [event.Event.Actor] because they answer a different question: the envelope's
// actor is who caused this occurrence, while a destination rendering "X
// assigned you this" needs the person behind that specific membership change,
// which only a system note records. Both are empty when the notes did not say.
type MRPayload struct {
	Assignees         []string         `json:"assignees"`
	Reviewers         []string         `json:"reviewers"`
	AssignedBy        chunkgitlab.User `json:"assigned_by,omitzero"`
	ReviewRequestedBy chunkgitlab.User `json:"review_requested_by,omitzero"`
	// AssignedAt and ReviewRequestedAt are when those changes happened. Zero
	// when the system notes did not say, which a destination must read as
	// "unknown", not as "old".
	AssignedAt        time.Time `json:"assigned_at,omitzero"`
	ReviewRequestedAt time.Time `json:"review_requested_at,omitzero"`
	// Comments are the merge request's newest comments, oldest first (see
	// [latestComments]). Like the member sets they are current state, not a
	// diff: the destination decides which of them are news, keyed by comment
	// id.
	Comments []Comment `json:"comments,omitempty"`
	// Resolutions are the merge request's resolved discussion threads. Like
	// the approvals they are current state, not a diff: a thread stays
	// resolved in every payload after it is closed, so a destination gates on
	// the resolution's own timestamp rather than on seeing it appear.
	Resolutions []Resolution `json:"resolutions,omitempty"`
	// Author is who opened the merge request. It rides here because the
	// author is the recipient of its terminal event — nobody else is told the
	// work is done — and because a destination must be able to tell an
	// unnamed author from one whose username GitLab omitted.
	Author chunkgitlab.User `json:"author,omitzero"`
	// Approvals are the merge request's standing approvals, oldest first.
	// Like the member sets they are current state, not a diff: an approval
	// stays in the payload on every poll after it is given, so a destination
	// gates on the approval's own timestamp rather than on seeing it appear.
	Approvals []Approval `json:"approvals,omitempty"`
	// State is the merge request's state as GitLab reports it ("opened",
	// "merged", "closed"), and MergedAt/MergedBy when and by whom it was
	// merged. Like everything else here they are current state: "merged" is
	// true on every poll from the merge onwards, so a destination gates on
	// MergedAt rather than on seeing the state change. MergedAt is zero
	// unless State is "merged".
	State    string           `json:"state,omitempty"`
	MergedAt time.Time        `json:"merged_at,omitzero"`
	MergedBy chunkgitlab.User `json:"merged_by,omitzero"`
}

// Approval is one standing approval as an event payload carries it: who gave
// it and when. The timestamp is what makes it projectable — an approval is
// current state, so a destination needs it to tell one just given from one
// standing for weeks.
type Approval struct {
	User chunkgitlab.User `json:"user,omitzero"`
	At   time.Time        `json:"at"`
}

// approvals maps the fetched merge request's approvals onto the payload shape.
func approvals(in []chunkgitlab.Approval) []Approval {
	out := make([]Approval, 0, len(in))
	for _, a := range in {
		out = append(out, Approval{User: a.User, At: a.At})
	}
	return out
}

// EventFromMergeRequest builds the canonical event for one fetched merge
// request: "this MR is in this state now", carrying its current members.
//
// It states current membership rather than a diff on purpose: destinations
// dedup (notify by the projected outbox key, ingest by content hash), so a
// re-fetched MR costs nothing and no fetch-side seen-set has to be persisted.
//
// The actor is the MR's last system-note author, not its opener: the author
// is fixed for the MR's whole life, so reporting them as the cause of every
// later update names the wrong person. It is zero when the notes name nobody,
// which renders as "Someone" rather than as a colleague who did nothing.
func EventFromMergeRequest(ref MergeRequestRef) (event.Event, error) {
	objectID := fmt.Sprintf("%s!%d", ref.Project, ref.MR.IID)
	e := event.Event{
		ID:      fmt.Sprintf("gitlab_mr_update:%s:%s", objectID, ref.MR.Updated.UTC().Format(time.RFC3339)),
		Source:  event.SourceGitLab,
		Type:    event.TypeMRUpdated,
		Subject: event.Ref{ID: objectID, URL: ref.MR.WebURL, Title: fmt.Sprintf("MR !%d: %s", ref.MR.IID, ref.MR.Title)},
		Actor: event.Actor{
			Key:     ref.MR.UpdatedBy.Username,
			Display: ref.MR.UpdatedBy.Display,
			URL:     ref.MR.UpdatedBy.URL,
		},
		OccurredAt: ref.MR.Updated,
		Attributes: map[string]string{"project": ref.Project},
	}
	e, err := e.WithPayload(MRPayloadVersion, MRPayload{
		Assignees:         ref.MR.Assignees,
		Reviewers:         ref.MR.Reviewers,
		AssignedBy:        ref.MR.AssignedBy,
		ReviewRequestedBy: ref.MR.ReviewRequestedBy,
		AssignedAt:        ref.MR.AssignedAt,
		ReviewRequestedAt: ref.MR.ReviewRequestedAt,
		Comments:          latestComments(ref.MR.Threads, ref.MR.WebURL),
		Resolutions:       resolutions(ref.MR.Threads, ref.MR.WebURL),
		Approvals:         approvals(ref.MR.Approvals),
		Author:            ref.MR.Author,
		State:             ref.MR.State,
		MergedAt:          ref.MR.MergedAt,
		MergedBy:          ref.MR.MergedBy,
	})
	if err != nil {
		return event.Event{}, errors.Wrap(err, "encode mr payload")
	}
	return e, nil
}

// ConflictPayloadVersion is [ConflictPayload]'s schema version; see
// [MRPayloadVersion] for what a bump means.
const ConflictPayloadVersion = 1

// ConflictPayload is the source-typed body of an [event.TypeMRConflict]
// event: who the conflict is addressed to, and what it is about.
//
// It carries no timestamp for the conflict itself because GitLab reports none
// — mergeability is standing state recomputed on demand. SHA stands in: it
// pins the source-branch head the verdict was made against, so a destination
// can tell "still the same conflict" from "pushed, still conflicting".
type ConflictPayload struct {
	Author       chunkgitlab.User `json:"author,omitzero"`
	Assignees    []string         `json:"assignees,omitempty"`
	SHA          string           `json:"sha,omitempty"`
	SourceBranch string           `json:"source_branch,omitempty"`
	TargetBranch string           `json:"target_branch,omitempty"`
	// Status is detailed_merge_status as GitLab reported it, for diagnostics.
	Status string `json:"status,omitempty"`
}

// EventFromConflict builds the canonical event for one conflicting merge
// request, observed at at.
//
// The event ID is keyed on the head SHA rather than on the observation time:
// every sweep re-reports a standing conflict, and keying on time would make
// each tick a fresh notification. The actor is deliberately zero — a conflict
// is caused by whoever moved the target branch, whom GitLab does not name
// here, and naming the wrong colleague is worse than naming none.
func EventFromConflict(ref ConflictRef, at time.Time) (event.Event, error) {
	objectID := fmt.Sprintf("%s!%d", ref.Project, ref.MR.IID)
	e := event.Event{
		ID:      fmt.Sprintf("gitlab_mr_conflict:%s:%s", objectID, ref.MR.SHA),
		Source:  event.SourceGitLab,
		Type:    event.TypeMRConflict,
		Subject: event.Ref{ID: objectID, URL: ref.MR.WebURL, Title: fmt.Sprintf("MR !%d: %s", ref.MR.IID, ref.MR.Title)},
		// The sweep's own clock: GitLab dates the MR, not the verdict.
		OccurredAt: at.UTC(),
		Attributes: map[string]string{"project": ref.Project},
	}
	e, err := e.WithPayload(ConflictPayloadVersion, ConflictPayload{
		Author:       ref.MR.Author,
		Assignees:    ref.MR.Assignees,
		SHA:          ref.MR.SHA,
		SourceBranch: ref.MR.SourceBranch,
		TargetBranch: ref.MR.TargetBranch,
		Status:       ref.MR.Status,
	})
	if err != nil {
		return event.Event{}, errors.Wrap(err, "encode conflict payload")
	}
	return e, nil
}
