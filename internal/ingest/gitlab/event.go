package gitlab

import (
	"fmt"
	"time"

	"github.com/go-faster/errors"

	"github.com/go-faster/sisyphus/internal/event"
)

// MRPayload is the source-typed body of an [event.TypeMRUpdated] event: the
// merge request's current member sets. Only a destination that understands
// GitLab decodes it — today the notification gateway's projector.
type MRPayload struct {
	Assignees []string `json:"assignees"`
	Reviewers []string `json:"reviewers"`
}

// EventFromMergeRequest builds the canonical event for one fetched merge
// request: "this MR is in this state now", carrying its current members.
//
// It states current membership rather than a diff on purpose: destinations
// dedup (notify by the projected outbox key, ingest by content hash), so a
// re-fetched MR costs nothing and no fetch-side seen-set has to be persisted.
func EventFromMergeRequest(ref MergeRequestRef) (event.Event, error) {
	objectID := fmt.Sprintf("%s!%d", ref.Project, ref.MR.IID)
	e := event.Event{
		ID:         fmt.Sprintf("gitlab_mr_update:%s:%s", objectID, ref.MR.Updated.UTC().Format(time.RFC3339)),
		Source:     event.SourceGitLab,
		Type:       event.TypeMRUpdated,
		Subject:    event.Ref{ID: objectID, URL: ref.MR.WebURL, Title: fmt.Sprintf("MR !%d: %s", ref.MR.IID, ref.MR.Title)},
		Actor:      event.Actor{Key: ref.MR.Author},
		OccurredAt: ref.MR.Updated,
		Attributes: map[string]string{"project": ref.Project},
	}
	e, err := e.WithPayload(MRPayload{Assignees: ref.MR.Assignees, Reviewers: ref.MR.Reviewers})
	if err != nil {
		return event.Event{}, errors.Wrap(err, "encode mr payload")
	}
	return e, nil
}
