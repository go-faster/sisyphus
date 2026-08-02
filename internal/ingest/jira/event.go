package jira

import (
	"fmt"
	"time"

	"github.com/go-faster/errors"

	chunkjira "github.com/go-faster/sisyphus/internal/chunk/jira"
	"github.com/go-faster/sisyphus/internal/event"
)

// IssuePayloadVersion is [IssuePayload]'s schema version. See
// [github.com/go-faster/sisyphus/internal/ingest/gitlab.MRPayloadVersion] for
// when to bump one.
const IssuePayloadVersion = 1

// IssuePayload is the source-typed body of an [event.TypeIssueUpdated] event:
// the issue's current assignee, and who put them there. Only a destination
// that understands Jira decodes it — today the notification gateway's
// projector.
//
// The assigner rides here rather than in [event.Event.Actor] because the two
// answer different questions: the envelope's actor is who caused this
// occurrence (the last person to touch the issue at all), while a destination
// rendering "X assigned you this" needs the person who set the assignee — who
// may have acted days earlier, and whom only Jira's changelog names. Every
// field is empty when the changelog did not say.
type IssuePayload struct {
	AssigneeAccountID string         `json:"assignee_account_id"`
	AssigneeDisplay   string         `json:"assignee_display"`
	AssignedBy        chunkjira.User `json:"assigned_by,omitzero"`
	// AssignedAt is when the assignee was set. Zero when the changelog did
	// not say, which a destination must read as "unknown", not as "old".
	AssignedAt time.Time `json:"assigned_at,omitzero"`
	// Comments are the issue's newest comments, oldest first (see
	// [latestComments]). Like the assignee they are current state, not a diff:
	// the destination decides which of them are news, keyed by comment id.
	Comments []Comment `json:"comments,omitempty"`
}

// EventFromIssue builds the canonical event for one fetched issue. Like
// [EventFromMergeRequest] in the GitLab adapter, it states the current
// assignee rather than a change to it.
//
// The actor is the issue's last changelog author, not its reporter: the
// reporter filed the issue once and stays fixed for its whole life, so
// reporting them as the cause of every later update names the wrong person.
// Both actors are zero on an issue with no usable changelog, which renders as
// "Someone" rather than as a colleague who did nothing.
func EventFromIssue(iss chunkjira.Issue) (event.Event, error) {
	e := event.Event{
		ID:         fmt.Sprintf("jira_issue_update:%s:%s", iss.Key, iss.Updated.UTC().Format(time.RFC3339)),
		Source:     event.SourceJira,
		Type:       event.TypeIssueUpdated,
		Subject:    event.Ref{ID: iss.Key, URL: iss.WebURL, Title: fmt.Sprintf("%s: %s", iss.Key, iss.Title)},
		Actor:      event.Actor{Key: iss.UpdatedBy.ID, Display: iss.UpdatedBy.Display, URL: iss.UpdatedBy.URL},
		OccurredAt: iss.Updated,
	}
	e, err := e.WithPayload(IssuePayloadVersion, IssuePayload{
		AssigneeAccountID: iss.AssigneeAccountID,
		AssigneeDisplay:   iss.Assignee,
		AssignedBy:        iss.AssignedBy,
		AssignedAt:        iss.AssignedAt,
		Comments:          latestComments(iss.Comments, iss.WebURL),
	})
	if err != nil {
		return event.Event{}, errors.Wrap(err, "encode issue payload")
	}
	return e, nil
}
