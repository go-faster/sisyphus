package gitlab

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	chunkgitlab "github.com/go-faster/sisyphus/internal/chunk/gitlab"
	ingestgitlab "github.com/go-faster/sisyphus/internal/ingest/gitlab"
	"github.com/go-faster/sisyphus/internal/notify"
)

// fakeFetcher serves canned pages, keyed by the incoming cursor's
// UpdatedAfter, so a test can simulate the collector re-polling with the
// cursor it advanced to on the previous call.
type fakeFetcher struct {
	pages map[string][]ingestgitlab.MergeRequestRef
}

func (f *fakeFetcher) FetchMergeRequestsStructured(_ context.Context, _ int, cursor ingestgitlab.Cursor) ([]ingestgitlab.MergeRequestRef, ingestgitlab.Cursor, bool, error) {
	refs := f.pages[cursor.UpdatedAfter]
	var maxUpdated string
	for _, r := range refs {
		if u := r.MR.Updated.Format(time.RFC3339); u > maxUpdated {
			maxUpdated = u
		}
	}
	if maxUpdated == "" {
		maxUpdated = cursor.UpdatedAfter
	}
	return refs, ingestgitlab.Cursor{UpdatedAfter: maxUpdated}, false, nil
}

func mr(assignees, reviewers []string, updated time.Time) chunkgitlab.MergeRequest {
	return chunkgitlab.MergeRequest{
		IID:       1,
		Title:     "Fix bug",
		Author:    "carol",
		WebURL:    "https://example.com/mr/1",
		Assignees: assignees,
		Reviewers: reviewers,
		Updated:   updated,
	}
}

func TestCollector_EmitsAssignedAndReviewRequested(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	fetcher := &fakeFetcher{pages: map[string][]ingestgitlab.MergeRequestRef{
		"": {{Project: "group/proj", MR: mr([]string{"alice"}, []string{"bob"}, t1)}},
	}}
	c := New(fetcher)

	events, cursor, err := c.Collect(t.Context(), "")
	require.NoError(t, err)
	require.NotEmpty(t, cursor)
	require.Len(t, events, 2)

	byType := map[notify.EventType]notify.Event{}
	for _, e := range events {
		byType[e.Type] = e
	}
	require.Equal(t, "alice", byType[notify.EventMRAssigned].Recipient.Key)
	require.Equal(t, "bob", byType[notify.EventMRReviewRequested].Recipient.Key)
	require.Equal(t, "carol", byType[notify.EventMRAssigned].Actor.Key)
	require.Equal(t, "gitlab_mr_assign:group/proj!1:alice", byType[notify.EventMRAssigned].EventID)
	require.Equal(t, "gitlab_mr_review:group/proj!1:bob", byType[notify.EventMRReviewRequested].EventID)
}

func TestCollector_ReRunWithAdvancedCursorEmitsNothingNew(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	unchanged := mr([]string{"alice"}, []string{"bob"}, t1)
	fetcher := &fakeFetcher{pages: map[string][]ingestgitlab.MergeRequestRef{
		"": {{Project: "group/proj", MR: unchanged}},
	}}
	c := New(fetcher)

	_, cursor1, err := c.Collect(t.Context(), "")
	require.NoError(t, err)

	// Same MR, same assignee/reviewer set returned again for the next poll
	// (keyed by the advanced cursor): idempotence — no new events.
	fetcher.pages[cursorUpdatedAfter(t, cursor1)] = []ingestgitlab.MergeRequestRef{
		{Project: "group/proj", MR: unchanged},
	}
	events, _, err := c.Collect(t.Context(), cursor1)
	require.NoError(t, err)
	require.Empty(t, events)
}

func TestCollector_OnlyNewlyAddedAssigneesFireEvents(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	fetcher := &fakeFetcher{pages: map[string][]ingestgitlab.MergeRequestRef{
		"": {{Project: "group/proj", MR: mr([]string{"alice"}, nil, t1)}},
	}}
	c := New(fetcher)

	_, cursor1, err := c.Collect(t.Context(), "")
	require.NoError(t, err)

	// alice stays assigned, dave is newly added: only dave should fire.
	fetcher.pages[cursorUpdatedAfter(t, cursor1)] = []ingestgitlab.MergeRequestRef{
		{Project: "group/proj", MR: mr([]string{"alice", "dave"}, nil, t2)},
	}
	events, _, err := c.Collect(t.Context(), cursor1)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, "dave", events[0].Recipient.Key)
}

// cursorUpdatedAfter extracts the state.UpdatedAfter a real cursor JSON
// carries, since fakeFetcher indexes its canned pages by that value.
func cursorUpdatedAfter(t *testing.T, cursor string) string {
	t.Helper()
	var st state
	require.NoError(t, json.Unmarshal([]byte(cursor), &st))
	return st.UpdatedAfter
}
