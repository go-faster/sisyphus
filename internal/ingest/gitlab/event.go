package gitlab

import (
	"fmt"
	"time"

	"github.com/go-faster/errors"

	chunkgitlab "github.com/go-faster/sisyphus/internal/chunk/gitlab"
	"github.com/go-faster/sisyphus/internal/event"
)

// MRPayload is the source-typed body of an [event.TypeMRUpdated] event: the
// merge request's current member sets, and who last put someone in them. Only
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
	e, err := e.WithPayload(MRPayload{
		Assignees:         ref.MR.Assignees,
		Reviewers:         ref.MR.Reviewers,
		AssignedBy:        ref.MR.AssignedBy,
		ReviewRequestedBy: ref.MR.ReviewRequestedBy,
		AssignedAt:        ref.MR.AssignedAt,
		ReviewRequestedAt: ref.MR.ReviewRequestedAt,
		Comments:          latestComments(ref.MR.Threads, ref.MR.WebURL),
	})
	if err != nil {
		return event.Event{}, errors.Wrap(err, "encode mr payload")
	}
	return e, nil
}
