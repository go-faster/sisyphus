package jira

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	chunkjira "github.com/go-faster/sisyphus/internal/chunk/jira"
	"github.com/go-faster/sisyphus/internal/event"
	ingestjira "github.com/go-faster/sisyphus/internal/ingest/jira"
	"github.com/go-faster/sisyphus/internal/notify"
)

func issueEvent(t *testing.T, accountID, display string) event.Event {
	t.Helper()
	return issueEventAssignedBy(t, accountID, display, ingestjira.IssuePayload{
		AssignedBy: chunkjira.User{
			ID:      "acc-rachel",
			Display: "Rachel",
			URL:     "https://jira.example.com/secure/ViewProfile.jspa?name=rachel",
		},
	})
}

// issueEventAssignedBy builds the event with the assigner fields taken from
// payload, so a test can say who assigned the issue — or that nobody known
// did.
func issueEventAssignedBy(t *testing.T, accountID, display string, payload ingestjira.IssuePayload) event.Event {
	t.Helper()
	e := event.Event{
		Source:  event.SourceJira,
		Type:    event.TypeIssueUpdated,
		Subject: event.Ref{ID: "IDP-1", URL: "https://jira.example.com/browse/IDP-1", Title: "IDP-1: Fix bug"},
		// The envelope's actor is whoever touched the issue last — which is
		// deliberately not who the notification names.
		Actor:      event.Actor{Key: "acc-dave", Display: "Dave", URL: "https://jira.example.com/secure/ViewProfile.jspa?name=dave"},
		OccurredAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}
	payload.AssigneeAccountID = accountID
	payload.AssigneeDisplay = display
	e, err := e.WithPayload(payload)
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
	require.Equal(t, "acc-rachel", e.Actor.Key)
	require.Equal(t, "IDP-1: Fix bug", e.Title)
	require.Equal(t, "jira_assign:IDP-1:acc-alice", e.EventID)
	require.Equal(t, "https://jira.example.com/secure/ViewProfile.jspa?name=rachel", e.Actor.URL)
	require.Equal(t, []notify.Button{{Text: "Open issue", URL: "https://jira.example.com/browse/IDP-1"}}, e.Buttons)
}

// The notification names the assigner, never whoever merely touched the issue
// last — that is the envelope's actor, and using it would credit a label edit
// as an assignment.
func TestProjector_ActorIsAssignerNotLastUpdater(t *testing.T) {
	events, err := Projector{}.Project(issueEvent(t, "acc-alice", "Alice"))
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.NotEqual(t, "Dave", events[0].Actor.Display)
}

// With no known assigner the actor stays zero, which renders as "Someone":
// naming the wrong colleague is worse than naming none.
func TestProjector_UnknownAssignerLeavesActorUnset(t *testing.T) {
	events, err := Projector{}.Project(issueEventAssignedBy(t, "acc-alice", "Alice", ingestjira.IssuePayload{}))
	require.NoError(t, err)
	require.Len(t, events, 1)

	e := events[0]
	require.Empty(t, e.Actor.Key)
	require.Empty(t, e.Actor.Display)
	require.Empty(t, e.Actor.URL)
	require.False(t, e.SelfCaused())
}

// Assigning an issue to yourself is self-caused, so the dispatcher skips it.
// This only works because the actor's key comes from the same identity space
// as the recipient's.
func TestProjector_SelfAssignmentIsSelfCaused(t *testing.T) {
	events, err := Projector{}.Project(issueEventAssignedBy(t, "acc-alice", "Alice", ingestjira.IssuePayload{
		AssignedBy: chunkjira.User{ID: "acc-alice", Display: "Alice"},
	}))
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.True(t, events[0].SelfCaused())
}

func TestProjector_UnassignedIssueProjectsNothing(t *testing.T) {
	events, err := Projector{}.Project(issueEvent(t, "", ""))
	require.NoError(t, err)
	require.Empty(t, events)
}
