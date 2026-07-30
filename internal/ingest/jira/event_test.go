package jira

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	chunkjira "github.com/go-faster/sisyphus/internal/chunk/jira"
	"github.com/go-faster/sisyphus/internal/event"
)

func TestEventFromIssue(t *testing.T) {
	updated := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	iss := chunkjira.Issue{
		Key:               "ABC-1",
		Title:             "Broken dashboard",
		Reporter:          "Carol",
		Assignee:          "Alice",
		AssigneeAccountID: "acc-alice",
		WebURL:            "https://jira.example.com/browse/ABC-1",
		Updated:           updated,
	}

	e, err := EventFromIssue(iss)
	require.NoError(t, err)

	require.Equal(t, event.SourceJira, e.Source)
	require.Equal(t, event.TypeIssueUpdated, e.Type)
	require.Equal(t, "jira_issue_update:ABC-1:2026-03-04T05:06:07Z", e.ID)
	require.Equal(t, "ABC-1", e.Subject.ID)
	require.Equal(t, iss.WebURL, e.Subject.URL)
	require.Equal(t, "ABC-1: Broken dashboard", e.Subject.Title)
	require.Equal(t, "Carol", e.Actor.Display)
	require.Equal(t, updated, e.OccurredAt)

	var p IssuePayload
	require.NoError(t, e.DecodePayload(&p))
	require.Equal(t, "acc-alice", p.AssigneeAccountID)
	require.Equal(t, "Alice", p.AssigneeDisplay)
}

// Unassigned issues still produce an event: the knowledge-graph destination
// wants them, and it is the notify projector — not the source adapter — that
// decides an issue with no assignee notifies nobody.
func TestEventFromIssueUnassigned(t *testing.T) {
	e, err := EventFromIssue(chunkjira.Issue{Key: "ABC-2", Updated: time.Unix(0, 0).UTC()})
	require.NoError(t, err)

	var p IssuePayload
	require.NoError(t, e.DecodePayload(&p))
	require.Empty(t, p.AssigneeAccountID)
}
