package jira

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/event"
	"github.com/go-faster/sisyphus/internal/notify"
)

func issueEvent(t *testing.T, accountID, display string) event.Event {
	t.Helper()
	e := event.Event{
		Source:     event.SourceJira,
		Type:       event.TypeIssueUpdated,
		Subject:    event.Ref{ID: "IDP-1", URL: "https://jira.example.com/browse/IDP-1", Title: "IDP-1: Fix bug"},
		Actor:      event.Actor{Display: "Rachel"},
		OccurredAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}
	e, err := e.WithPayload(IssuePayload{AssigneeAccountID: accountID, AssigneeDisplay: display})
	require.NoError(t, err)
	return e
}

func TestProjector_AssignedIssueBecomesNotification(t *testing.T) {
	events, err := Projector{}.Project(issueEvent(t, "acc-alice", "Alice"))
	require.NoError(t, err)
	require.Len(t, events, 1)

	e := events[0]
	require.Equal(t, notify.EventIssueAssigned, e.Type)
	require.Equal(t, "acc-alice", e.Recipient.Key)
	require.Equal(t, "Alice", e.Recipient.Display)
	require.Equal(t, "Rachel", e.Actor.Display)
	require.Equal(t, "IDP-1: Fix bug", e.Title)
	require.Equal(t, "jira_assign:IDP-1:acc-alice", e.EventID)
}

func TestProjector_UnassignedIssueProjectsNothing(t *testing.T) {
	events, err := Projector{}.Project(issueEvent(t, "", ""))
	require.NoError(t, err)
	require.Empty(t, events)
}
