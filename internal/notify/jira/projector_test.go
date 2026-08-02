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

// eventTime is the fixture clock. The projector's staleness check needs a
// "now" near the event, or every fixture would look long stale.
var eventTime = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// testProjector is the projector with its clock pinned to eventTime.
func testProjector() Projector {
	return Projector{Staleness: notify.Staleness{Now: func() time.Time { return eventTime }}}
}

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
		OccurredAt: eventTime,
	}
	payload.AssigneeAccountID = accountID
	payload.AssigneeDisplay = display
	if payload.AssignedAt.IsZero() {
		payload.AssignedAt = e.OccurredAt
	}
	e, err := e.WithPayload(ingestjira.IssuePayloadVersion, payload)
	require.NoError(t, err)
	return e
}

func TestProjector_AssignedIssueBecomesNotification(t *testing.T) {
	events, err := testProjector().Project(issueEvent(t, "acc-alice", "Alice"))
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
	events, err := testProjector().Project(issueEvent(t, "acc-alice", "Alice"))
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.NotEqual(t, "Dave", events[0].Actor.Display)
}

// With no known assigner the actor stays zero, which renders as "Someone":
// naming the wrong colleague is worse than naming none.
func TestProjector_UnknownAssignerLeavesActorUnset(t *testing.T) {
	events, err := testProjector().Project(issueEventAssignedBy(t, "acc-alice", "Alice", ingestjira.IssuePayload{}))
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
	events, err := testProjector().Project(issueEventAssignedBy(t, "acc-alice", "Alice", ingestjira.IssuePayload{
		AssignedBy: chunkjira.User{ID: "acc-alice", Display: "Alice"},
	}))
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.True(t, events[0].SelfCaused())
}

func TestProjector_UnassignedIssueProjectsNothing(t *testing.T) {
	events, err := testProjector().Project(issueEvent(t, "", ""))
	require.NoError(t, err)
	require.Empty(t, events)
}

// An assignment the changelog dates to months ago notifies nobody: the event
// states the issue's current assignee, so an unrelated edit would otherwise
// announce it as if it just happened.
func TestProjector_StaleAssignmentProjectsNothing(t *testing.T) {
	occurred := eventTime
	e := issueEventAssignedBy(t, "acc-alice", "Alice", ingestjira.IssuePayload{
		AssignedBy: chunkjira.User{ID: "acc-rachel", Display: "Rachel"},
		AssignedAt: occurred.AddDate(0, -3, 0),
	})

	p := Projector{Staleness: notify.Staleness{Now: func() time.Time { return occurred }}}
	events, err := p.Project(e)
	require.NoError(t, err)
	require.Empty(t, events)

	// The same event notifies once the check is disabled, so the drop is the
	// cutoff talking and not a decode failure.
	p.Staleness.MaxAge = -1
	events, err = p.Project(e)
	require.NoError(t, err)
	require.Len(t, events, 1)
}

// A changelog that never named the assignment still notifies: losing a real
// assignment is worse than one extra message that dedup collapses anyway.
func TestProjector_UnknownAssignmentTimeStillNotifies(t *testing.T) {
	occurred := eventTime
	e := issueEventAssignedBy(t, "acc-alice", "Alice", ingestjira.IssuePayload{
		AssignedBy: chunkjira.User{ID: "acc-rachel", Display: "Rachel"},
		AssignedAt: time.Time{},
	})
	// issueEventAssignedBy defaults AssignedAt to the event time, so clear it
	// back out: this test is about a payload that carries none at all.
	var decoded ingestjira.IssuePayload
	require.NoError(t, e.DecodePayload(ingestjira.IssuePayloadVersion, &decoded))
	decoded.AssignedAt = time.Time{}
	e, err := e.WithPayload(ingestjira.IssuePayloadVersion, decoded)
	require.NoError(t, err)

	p := Projector{Staleness: notify.Staleness{Now: func() time.Time { return occurred.AddDate(1, 0, 0) }}}
	events, err := p.Project(e)
	require.NoError(t, err)
	require.Len(t, events, 1)
}

// commentEvent builds an issue event whose only news is its comments: the
// assignment is dated months back so it never confuses the assertions.
func commentEvent(t *testing.T, assignee string, comments ...ingestjira.Comment) event.Event {
	t.Helper()
	return issueEventAssignedBy(t, assignee, "Alice", ingestjira.IssuePayload{
		AssignedBy: chunkjira.User{ID: "acc-rachel"},
		AssignedAt: eventTime.AddDate(0, -3, 0),
		Comments:   comments,
	})
}

func TestProjector_ProjectsComments(t *testing.T) {
	e := commentEvent(t, "acc-alice", ingestjira.Comment{
		ID:        "9001",
		Author:    chunkjira.User{ID: "acc-carol", Display: "Carol", URL: "https://jira.example.com/carol"},
		Body:      "any update?",
		URL:       "https://jira.example.com/browse/IDP-1?focusedCommentId=9001",
		CreatedAt: eventTime,
	})

	events, err := testProjector().Project(e)
	require.NoError(t, err)
	require.Len(t, events, 1)

	got := events[0]
	require.Equal(t, notify.EventIssueCommented, got.Type)
	require.Equal(t, "acc-alice", got.Recipient.Key)
	require.Equal(t, "acc-carol", got.Actor.Key)
	require.Equal(t, "any update?", got.Description)
	require.Equal(t, "issue_commented:IDP-1:9001:acc-alice", got.EventID)
	require.Equal(t, []notify.Button{{
		Text: "Open comment",
		URL:  "https://jira.example.com/browse/IDP-1?focusedCommentId=9001",
	}}, got.Buttons)
}

// Being named reaches someone with no relationship to the issue at all — an
// unassigned issue included, which is why the projector no longer returns
// early on one.
func TestProjector_ProjectsMentionsOnUnassignedIssue(t *testing.T) {
	e := commentEvent(t, "", ingestjira.Comment{
		ID:        "9001",
		Author:    chunkjira.User{ID: "acc-carol"},
		Body:      "[~acc-erin] thoughts?",
		Mentions:  []string{"acc-erin"},
		CreatedAt: eventTime,
	})

	events, err := testProjector().Project(e)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, notify.EventIssueMentioned, events[0].Type)
	require.Equal(t, "acc-erin", events[0].Recipient.Key)
	require.Equal(t, notify.SourceJira, events[0].Recipient.Source)
	require.Equal(t, "issue_mentioned:IDP-1:9001:acc-erin", events[0].EventID)
}

// A comment on an issue assigned to you months ago is still news, even though
// the assignment behind it is not.
func TestProjector_CommentsOutliveStaleAssignment(t *testing.T) {
	e := commentEvent(t, "acc-alice", ingestjira.Comment{
		ID:        "9001",
		Author:    chunkjira.User{ID: "acc-carol"},
		Body:      "ping",
		CreatedAt: eventTime,
	})

	events, err := testProjector().Project(e)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, notify.EventIssueCommented, events[0].Type)
}

// Your own comment is self-caused, so the dispatcher drops it.
func TestProjector_OwnCommentIsSelfCaused(t *testing.T) {
	e := commentEvent(t, "", ingestjira.Comment{
		ID:        "9001",
		Author:    chunkjira.User{ID: "acc-erin"},
		Body:      "[~acc-erin] note to self",
		Mentions:  []string{"acc-erin"},
		CreatedAt: eventTime,
	})

	events, err := testProjector().Project(e)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.True(t, events[0].SelfCaused())
}
