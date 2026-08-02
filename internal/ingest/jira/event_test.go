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
		UpdatedBy:         chunkjira.User{ID: "acc-dave", Display: "Dave", URL: "https://jira.example.com/jira/people/acc-dave"},
		AssignedBy:        chunkjira.User{ID: "acc-bob", Display: "Bob", URL: "https://jira.example.com/jira/people/acc-bob"},
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
	require.Equal(t, updated, e.OccurredAt)

	// The actor is the last changelog author, never the reporter.
	require.Equal(t, "acc-dave", e.Actor.Key)
	require.Equal(t, "Dave", e.Actor.Display)
	require.Equal(t, "https://jira.example.com/jira/people/acc-dave", e.Actor.URL)
	require.NotEqual(t, iss.Reporter, e.Actor.Display)

	var p IssuePayload
	require.NoError(t, e.DecodePayload(IssuePayloadVersion, &p))
	require.Equal(t, "acc-alice", p.AssigneeAccountID)
	require.Equal(t, "Alice", p.AssigneeDisplay)
	require.Equal(t, chunkjira.User{
		ID:      "acc-bob",
		Display: "Bob",
		URL:     "https://jira.example.com/jira/people/acc-bob",
	}, p.AssignedBy)
}

// An issue whose changelog named nobody carries no actor at all, rather than
// falling back to the reporter.
func TestEventFromIssueWithoutChangelogHasNoActor(t *testing.T) {
	e, err := EventFromIssue(chunkjira.Issue{
		Key:               "ABC-3",
		Reporter:          "Carol",
		ReporterURL:       "https://jira.example.com/jira/people/acc-carol",
		Assignee:          "Alice",
		AssigneeAccountID: "acc-alice",
		Updated:           time.Unix(0, 0).UTC(),
	})
	require.NoError(t, err)
	require.True(t, e.Actor.Zero())

	var p IssuePayload
	require.NoError(t, e.DecodePayload(IssuePayloadVersion, &p))
	require.True(t, p.AssignedBy.Zero())
}

// Unassigned issues still produce an event: the knowledge-graph destination
// wants them, and it is the notify projector — not the source adapter — that
// decides an issue with no assignee notifies nobody.
func TestEventFromIssueUnassigned(t *testing.T) {
	e, err := EventFromIssue(chunkjira.Issue{Key: "ABC-2", Updated: time.Unix(0, 0).UTC()})
	require.NoError(t, err)

	var p IssuePayload
	require.NoError(t, e.DecodePayload(IssuePayloadVersion, &p))
	require.Empty(t, p.AssigneeAccountID)
}
