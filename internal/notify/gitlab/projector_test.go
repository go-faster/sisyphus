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
	e, err := e.WithPayload(ingestgitlab.MRPayloadVersion, payload)
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

// mergedPayload is an MR payload whose only news is the merge: its membership
// is old, so an assignment event never confuses the assertions.
func mergedPayload(author string, assignees []string, mergedBy string, mergedAt time.Time) ingestgitlab.MRPayload {
	stale := eventTime.AddDate(0, -3, 0)
	return ingestgitlab.MRPayload{
		Assignees:         assignees,
		AssignedAt:        stale,
		ReviewRequestedAt: stale,
		Author:            chunkgitlab.User{Username: author},
		State:             "merged",
		MergedAt:          mergedAt,
		MergedBy:          chunkgitlab.User{Username: mergedBy, Display: "Carol", URL: "https://example.com/carol"},
	}
}

func TestProjector_ProjectsMerge(t *testing.T) {
	mergedAt := eventTime.Add(-time.Hour)
	e := mrEventWithActors(t, mergedPayload("alice", []string{"bob"}, "carol", mergedAt))

	events, err := testProjector().Project(e)
	require.NoError(t, err)
	require.Len(t, events, 2)

	// The author first: an MR landing is news to whoever wrote it before it is
	// news to anyone else.
	author := events[0]
	require.Equal(t, notify.EventMRMerged, author.Type)
	require.Equal(t, "alice", author.Recipient.Key)
	require.Equal(t, "carol", author.Actor.Key)
	require.Equal(t, "https://example.com/carol", author.Actor.URL)
	require.Equal(t, "gitlab_mr_merged:group/proj!1:alice", author.EventID)
	require.Equal(t, mergedAt, author.OccurredAt)
	require.Equal(t, []notify.Button{{Text: "Open merge request", URL: "https://example.com/mr/1"}}, author.Buttons)

	require.Equal(t, notify.EventMRMerged, events[1].Type)
	require.Equal(t, "bob", events[1].Recipient.Key)
	require.Equal(t, "gitlab_mr_merged:group/proj!1:bob", events[1].EventID)
}

// An open MR carries the same current-state payload every poll and must not
// announce a merge that has not happened.
func TestProjector_OpenMRProjectsNoMerge(t *testing.T) {
	e := mrEventWithActors(t, ingestgitlab.MRPayload{
		Assignees:  []string{"alice"},
		AssignedAt: eventTime.AddDate(0, -3, 0),
		Author:     chunkgitlab.User{Username: "alice"},
		State:      "opened",
	})

	events, err := testProjector().Project(e)
	require.NoError(t, err)
	require.Empty(t, events)
}

// "merged" stays true forever, so the merge is gated on its timestamp: an MR
// merged months ago notifies nobody when it is next touched.
func TestProjector_StaleMergeProjectsNothing(t *testing.T) {
	e := mrEventWithActors(t, mergedPayload("alice", nil, "carol", eventTime.AddDate(0, -3, 0)))

	events, err := testProjector().Project(e)
	require.NoError(t, err)
	require.Empty(t, events)
}

// Merging your own MR is self-caused, so the dispatcher drops it — which only
// works because the merger is a username and never a display name.
func TestProjector_SelfMergeIsSelfCaused(t *testing.T) {
	e := mrEventWithActors(t, mergedPayload("alice", nil, "alice", eventTime))

	events, err := testProjector().Project(e)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.True(t, events[0].SelfCaused())
}

// An author who is also an assignee hears about the merge once.
func TestProjector_MergeDedupsAuthorAmongAssignees(t *testing.T) {
	e := mrEventWithActors(t, mergedPayload("alice", []string{"alice", "bob"}, "carol", eventTime))

	events, err := testProjector().Project(e)
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Equal(t, "alice", events[0].Recipient.Key)
	require.Equal(t, "bob", events[1].Recipient.Key)
}

// GitLab omitting the author's username leaves nobody to address: a display
// name is not a match key, and addressing one would reach whoever holds that
// string as a username.
func TestProjector_MergeWithoutAuthorUsernameAddressesAssigneesOnly(t *testing.T) {
	p := mergedPayload("", []string{"bob"}, "carol", eventTime)
	p.Author = chunkgitlab.User{Display: "Alice Example"}
	e := mrEventWithActors(t, p)

	events, err := testProjector().Project(e)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, "bob", events[0].Recipient.Key)
}

// commentPayload is an MR payload whose only news is its comments: no
// membership actors, so an assignment event never confuses the assertions.
func commentPayload(assignees, reviewers []string, comments ...ingestgitlab.Comment) ingestgitlab.MRPayload {
	stale := eventTime.AddDate(0, -3, 0)
	return ingestgitlab.MRPayload{
		Assignees:         assignees,
		Reviewers:         reviewers,
		AssignedAt:        stale,
		ReviewRequestedAt: stale,
		Comments:          comments,
	}
}

func TestProjector_ProjectsComments(t *testing.T) {
	e := mrEventWithActors(t, commentPayload([]string{"alice"}, []string{"bob"},
		ingestgitlab.Comment{
			ID:        "7",
			Author:    chunkgitlab.User{Username: "carol", Display: "Carol", URL: "https://example.com/carol"},
			Body:      "needs a rebase",
			URL:       "https://example.com/mr/1#note_7",
			CreatedAt: eventTime,
		},
	))

	events, err := testProjector().Project(e)
	require.NoError(t, err)
	require.Len(t, events, 2)

	for _, got := range events {
		require.Equal(t, notify.EventMRCommented, got.Type)
		require.Equal(t, "carol", got.Actor.Key)
		require.Equal(t, "needs a rebase", got.Description)
		require.Equal(t, "https://example.com/mr/1#note_7", got.URL)
	}
	require.Equal(t, "mr_commented:group/proj!1:7:alice", events[0].EventID)
	require.Equal(t, "mr_commented:group/proj!1:7:bob", events[1].EventID)
}

// A comment on an MR assigned to you months ago is still news, even though
// the assignment behind it is not.
func TestProjector_CommentsOutliveStaleAssignment(t *testing.T) {
	e := mrEventWithActors(t, commentPayload([]string{"alice"}, nil,
		ingestgitlab.Comment{ID: "7", Author: chunkgitlab.User{Username: "carol"}, Body: "ping", CreatedAt: eventTime},
	))

	events, err := testProjector().Project(e)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, notify.EventMRCommented, events[0].Type)
}

// Being named reaches someone with no other relationship to the MR.
func TestProjector_ProjectsMentions(t *testing.T) {
	e := mrEventWithActors(t, commentPayload(nil, nil,
		ingestgitlab.Comment{
			ID:        "7",
			Author:    chunkgitlab.User{Username: "carol"},
			Body:      "@erin thoughts?",
			Mentions:  []string{"erin"},
			CreatedAt: eventTime,
		},
	))

	events, err := testProjector().Project(e)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, notify.EventMRMentioned, events[0].Type)
	require.Equal(t, "erin", events[0].Recipient.Key)
	require.Equal(t, notify.SourceGitLab, events[0].Recipient.Source)
	require.Equal(t, "mr_mentioned:group/proj!1:7:erin", events[0].EventID)
}

// Your own comment is self-caused, so the dispatcher drops it.
func TestProjector_OwnCommentIsSelfCaused(t *testing.T) {
	e := mrEventWithActors(t, commentPayload(nil, nil,
		ingestgitlab.Comment{
			ID:        "7",
			Author:    chunkgitlab.User{Username: "erin"},
			Body:      "@erin note to self",
			Mentions:  []string{"erin"},
			CreatedAt: eventTime,
		},
	))

	events, err := testProjector().Project(e)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.True(t, events[0].SelfCaused())
}
