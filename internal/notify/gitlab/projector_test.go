package gitlab

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/event"
	ingestgitlab "github.com/go-faster/sisyphus/internal/ingest/gitlab"
	"github.com/go-faster/sisyphus/internal/notify"
)

func mrEvent(t *testing.T, assignees, reviewers []string) event.Event {
	t.Helper()
	e := event.Event{
		Source:     event.SourceGitLab,
		Type:       event.TypeMRUpdated,
		Subject:    event.Ref{ID: "group/proj!1", URL: "https://example.com/mr/1", Title: "MR !1: Fix bug"},
		Actor:      event.Actor{Key: "carol", URL: "https://example.com/carol"},
		OccurredAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}
	e, err := e.WithPayload(ingestgitlab.MRPayload{Assignees: assignees, Reviewers: reviewers})
	require.NoError(t, err)
	return e
}

func TestProjector_FansOutAssigneesAndReviewers(t *testing.T) {
	events, err := Projector{}.Project(mrEvent(t, []string{"alice"}, []string{"bob"}))
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
}

func TestProjector_NoMembersProjectsNothing(t *testing.T) {
	events, err := Projector{}.Project(mrEvent(t, nil, nil))
	require.NoError(t, err)
	require.Empty(t, events)
}
