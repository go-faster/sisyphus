package gitlab

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	chunkgitlab "github.com/go-faster/sisyphus/internal/chunk/gitlab"
	"github.com/go-faster/sisyphus/internal/event"
	ingestgitlab "github.com/go-faster/sisyphus/internal/ingest/gitlab"
)

// fakeFetcher serves canned pages, keyed by the incoming cursor's
// UpdatedAfter, so a test can simulate the collector re-polling with the
// cursor it advanced to on the previous call.
type fakeFetcher struct {
	pages map[string][]ingestgitlab.MergeRequestRef
}

func (f *fakeFetcher) FetchMergeRequests(_ context.Context, _ int, cursor ingestgitlab.Cursor) ([]ingestgitlab.MergeRequestRef, ingestgitlab.Cursor, bool, error) {
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

// The collector emits one canonical event per MR, carrying the current member
// sets in its payload — not one event per recipient. The per-recipient fan-out
// is the Projector's job (see projector_test.go).
func TestCollector_EmitsMRUpdatedEvent(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	fetcher := &fakeFetcher{pages: map[string][]ingestgitlab.MergeRequestRef{
		"": {{Project: "group/proj", MR: mr([]string{"alice"}, []string{"bob"}, t1)}},
	}}
	c := New(fetcher)

	events, cursor, err := c.Collect(t.Context(), "")
	require.NoError(t, err)
	require.NotEmpty(t, cursor)
	require.Len(t, events, 1)

	e := events[0]
	require.Equal(t, event.SourceGitLab, e.Source)
	require.Equal(t, event.TypeMRUpdated, e.Type)
	require.Equal(t, "group/proj!1", e.Subject.ID)
	require.Equal(t, "https://example.com/mr/1", e.Subject.URL)
	require.Equal(t, "MR !1: Fix bug", e.Subject.Title)
	require.Equal(t, "carol", e.Actor.Key)
	require.Equal(t, t1, e.OccurredAt)
	require.Equal(t, "group/proj", e.Attr("project"))

	var p MRPayload
	require.NoError(t, e.DecodePayload(&p))
	require.Equal(t, []string{"alice"}, p.Assignees)
	require.Equal(t, []string{"bob"}, p.Reviewers)
}

// A re-poll with the advanced cursor fetches nothing new (the fetcher has no
// page for it), so the collector emits nothing — the cursor bounds the fetch.
func TestCollector_AdvancedCursorFetchesNothing(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	fetcher := &fakeFetcher{pages: map[string][]ingestgitlab.MergeRequestRef{
		"": {{Project: "group/proj", MR: mr([]string{"alice"}, []string{"bob"}, t1)}},
	}}
	c := New(fetcher)

	_, cursor1, err := c.Collect(t.Context(), "")
	require.NoError(t, err)

	events, _, err := c.Collect(t.Context(), cursor1)
	require.NoError(t, err)
	require.Empty(t, events)
	require.NotEqual(t, "", cursorUpdatedAfter(t, cursor1))
}

// cursorUpdatedAfter extracts the state.UpdatedAfter a real cursor JSON
// carries, since fakeFetcher indexes its canned pages by that value.
func cursorUpdatedAfter(t *testing.T, cursor string) string {
	t.Helper()
	var st state
	require.NoError(t, json.Unmarshal([]byte(cursor), &st))
	return st.UpdatedAfter
}
