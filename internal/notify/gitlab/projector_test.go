package gitlab

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	chunkgitlab "github.com/go-faster/sisyphus/internal/chunk/gitlab"
	"github.com/go-faster/sisyphus/internal/event"
	ingestgitlab "github.com/go-faster/sisyphus/internal/ingest/gitlab"
	"github.com/go-faster/sisyphus/internal/notify"
)

// eventTime is the fixture clock. The projector's staleness check needs a
// "now" near the event, or every fixture would look long stale.
var eventTime = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// testProjector is the projector with its clock pinned to eventTime.
func testProjector() Projector {
	return Projector{Staleness: notify.Staleness{Now: func() time.Time { return eventTime }}}
}

func mrEvent(t *testing.T, assignees, reviewers []string) event.Event {
	t.Helper()
	return mrEventWithActors(t, ingestgitlab.MRPayload{
		Assignees:         assignees,
		Reviewers:         reviewers,
		AssignedBy:        chunkgitlab.User{Username: "carol", URL: "https://example.com/carol"},
		ReviewRequestedBy: chunkgitlab.User{Username: "dave", URL: "https://example.com/dave"},
	})
}

// mrEventWithActors builds the event with the membership actors taken from
// payload, so a test can say who assigned or requested — or that nobody known
// did.
func mrEventWithActors(t *testing.T, payload ingestgitlab.MRPayload) event.Event {
	t.Helper()
	e := event.Event{
		Source:  event.SourceGitLab,
		Type:    event.TypeMRUpdated,
		Subject: event.Ref{ID: "group/proj!1", URL: "https://example.com/mr/1", Title: "MR !1: Fix bug"},
		// The envelope's actor is whoever touched the MR last, which is
		// deliberately not who the notification names.
		Actor:      event.Actor{Key: "erin", URL: "https://example.com/erin"},
		OccurredAt: eventTime,
	}
	if payload.AssignedAt.IsZero() {
		payload.AssignedAt = e.OccurredAt
	}
	if payload.ReviewRequestedAt.IsZero() {
		payload.ReviewRequestedAt = e.OccurredAt
	}
	e, err := e.WithPayload(payload)
	require.NoError(t, err)
	return e
}

func TestProjector_FansOutAssigneesAndReviewers(t *testing.T) {
	events, err := testProjector().Project(mrEvent(t, []string{"alice"}, []string{"bob"}))
	require.NoError(t, err)
	require.Len(t, events, 2)

	byType := map[notify.EventType]notify.Event{}
	for _, e := range events {
		byType[e.Type] = e
	}

	assigned := byType[notify.EventMRAssigned]
	require.Equal(t, "alice", assigned.Recipient.Key)
	require.Equal(t, "carol", assigned.Actor.Key)
	require.Equal(t, "MR !1: Fix bug", assigned.Title)
	require.Equal(t, "gitlab_mr_assign:group/proj!1:alice", assigned.EventID)
	// The profile link the source adapter carried, so the message can name
	// the actor as a link instead of bare text.
	require.Equal(t, "https://example.com/carol", assigned.Actor.URL)
	require.Equal(t, []notify.Button{{Text: "Open merge request", URL: "https://example.com/mr/1"}}, assigned.Buttons)

	review := byType[notify.EventMRReviewRequested]
	require.Equal(t, "bob", review.Recipient.Key)
	require.Equal(t, "gitlab_mr_review:group/proj!1:bob", review.EventID)
	// The requester, not the assigner and not the last person to touch the MR.
	require.Equal(t, "dave", review.Actor.Key)
}

// With no known actor the notification names nobody, which renders as
// "Someone", rather than crediting whoever touched the MR last.
func TestProjector_UnknownActorLeavesActorUnset(t *testing.T) {
	events, err := testProjector().Project(mrEventWithActors(t, ingestgitlab.MRPayload{Assignees: []string{"alice"}}))
	require.NoError(t, err)
	require.Len(t, events, 1)

	e := events[0]
	require.Empty(t, e.Actor.Key)
	require.Empty(t, e.Actor.URL)
	require.False(t, e.SelfCaused())
}

// Assigning an MR to yourself is self-caused, so the dispatcher skips it.
func TestProjector_SelfAssignmentIsSelfCaused(t *testing.T) {
	events, err := testProjector().Project(mrEventWithActors(t, ingestgitlab.MRPayload{
		Assignees:  []string{"alice"},
		AssignedBy: chunkgitlab.User{Username: "alice"},
	}))
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.True(t, events[0].SelfCaused())
}

func TestProjector_NoMembersProjectsNothing(t *testing.T) {
	events, err := testProjector().Project(mrEvent(t, nil, nil))
	require.NoError(t, err)
	require.Empty(t, events)
}

// Membership changes the payload dates to months ago notify nobody, per
// membership kind: a stale assignment must not silence a fresh review
// request on the same MR.
func TestProjector_StaleMembershipProjectsNothing(t *testing.T) {
	occurred := eventTime
	e := mrEventWithActors(t, ingestgitlab.MRPayload{
		Assignees:         []string{"alice"},
		Reviewers:         []string{"bob"},
		AssignedBy:        chunkgitlab.User{Username: "carol"},
		ReviewRequestedBy: chunkgitlab.User{Username: "dave"},
		AssignedAt:        occurred.AddDate(0, -3, 0),
		ReviewRequestedAt: occurred,
	})

	p := Projector{Staleness: notify.Staleness{Now: func() time.Time { return occurred }}}
	events, err := p.Project(e)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, notify.EventMRReviewRequested, events[0].Type)
}
