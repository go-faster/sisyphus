package jira

import (
	"fmt"
	"time"

	"github.com/go-faster/errors"

	chunkjira "github.com/go-faster/sisyphus/internal/chunk/jira"
	"github.com/go-faster/sisyphus/internal/event"
)

// IssuePayload is the source-typed body of an [event.TypeIssueUpdated] event:
// the issue's current assignee. Only a destination that understands Jira
// decodes it — today the notification gateway's projector.
type IssuePayload struct {
	AssigneeAccountID string `json:"assignee_account_id"`
	AssigneeDisplay   string `json:"assignee_display"`
}

// EventFromIssue builds the canonical event for one fetched issue. Like
// [EventFromMergeRequest] in the GitLab adapter, it states the current
// assignee rather than a change to it.
func EventFromIssue(iss chunkjira.Issue) (event.Event, error) {
	e := event.Event{
		ID:         fmt.Sprintf("jira_issue_update:%s:%s", iss.Key, iss.Updated.UTC().Format(time.RFC3339)),
		Source:     event.SourceJira,
		Type:       event.TypeIssueUpdated,
		Subject:    event.Ref{ID: iss.Key, URL: iss.WebURL, Title: fmt.Sprintf("%s: %s", iss.Key, iss.Title)},
		Actor:      event.Actor{Display: iss.Reporter, URL: iss.ReporterURL},
		OccurredAt: iss.Updated,
	}
	e, err := e.WithPayload(IssuePayload{
		AssigneeAccountID: iss.AssigneeAccountID,
		AssigneeDisplay:   iss.Assignee,
	})
	if err != nil {
		return event.Event{}, errors.Wrap(err, "encode issue payload")
	}
	return e, nil
}
