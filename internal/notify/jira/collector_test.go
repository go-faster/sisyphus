package jira

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	chunkjira "github.com/go-faster/sisyphus/internal/chunk/jira"
	"github.com/go-faster/sisyphus/internal/event"
	ingestjira "github.com/go-faster/sisyphus/internal/ingest/jira"
)

// fakeFetcher serves canned pages keyed by the incoming cursor's
// LastUpdated, so a test can simulate the collector re-polling with the
// cursor it advanced to on the previous call.
type fakeFetcher struct {
	pages map[string][]chunkjira.Issue
}

func (f *fakeFetcher) FetchIssues(_ context.Context, _ ingestjira.FetchOptions, cursor ingestjira.Cursor) ([]chunkjira.Issue, ingestjira.Cursor, bool, error) {
	issues := f.pages[cursor.LastUpdated]
	var maxUpdated string
	for _, iss := range issues {
		if u := iss.Updated.Format(time.RFC3339); u > maxUpdated {
			maxUpdated = u
		}
	}
	if maxUpdated == "" {
		maxUpdated = cursor.LastUpdated
	}
	return issues, ingestjira.Cursor{LastUpdated: maxUpdated}, false, nil
}

const issueKey = "IDP-1"

func issue(assigneeAccountID, assigneeName string, updated time.Time) chunkjira.Issue {
	return chunkjira.Issue{
		Key:               issueKey,
		Title:             "Fix bug",
		Assignee:          assigneeName,
		AssigneeAccountID: assigneeAccountID,
		Reporter:          "Rachel",
		WebURL:            "https://jira.example.com/browse/" + issueKey,
		Updated:           updated,
	}
}

// The collector emits one canonical event per issue (assigned or not); the
// Projector decides whether it becomes a notification (see projector_test.go).
func TestCollector_EmitsIssueUpdatedEvent(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	fetcher := &fakeFetcher{pages: map[string][]chunkjira.Issue{
		"": {issue("acc-alice", "Alice", t1)},
	}}
	c := New(fetcher, []string{"IDP"})

	events, cursor, err := c.Collect(t.Context(), "")
	require.NoError(t, err)
	require.NotEmpty(t, cursor)
	require.Len(t, events, 1)

	e := events[0]
	require.Equal(t, event.SourceJira, e.Source)
	require.Equal(t, event.TypeIssueUpdated, e.Type)
	require.Equal(t, issueKey, e.Subject.ID)
	require.Equal(t, "IDP-1: Fix bug", e.Subject.Title)
	require.Equal(t, "Rachel", e.Actor.Display)
	require.Equal(t, t1, e.OccurredAt)

	var p IssuePayload
	require.NoError(t, e.DecodePayload(&p))
	require.Equal(t, "acc-alice", p.AssigneeAccountID)
	require.Equal(t, "Alice", p.AssigneeDisplay)
}

// An unassigned issue is still a canonical occurrence: the collector emits an
// event; only the Projector drops it.
func TestCollector_UnassignedIssueStillEmitsEvent(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	fetcher := &fakeFetcher{pages: map[string][]chunkjira.Issue{
		"": {issue("", "", t1)},
	}}
	c := New(fetcher, []string{"IDP"})

	events, _, err := c.Collect(t.Context(), "")
	require.NoError(t, err)
	require.Len(t, events, 1)

	var p IssuePayload
	require.NoError(t, events[0].DecodePayload(&p))
	require.Equal(t, "", p.AssigneeAccountID)
}

func TestCollector_AdvancedCursorFetchesNothing(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	fetcher := &fakeFetcher{pages: map[string][]chunkjira.Issue{
		"": {issue("acc-alice", "Alice", t1)},
	}}
	c := New(fetcher, []string{"IDP"})

	_, cursor1, err := c.Collect(t.Context(), "")
	require.NoError(t, err)

	events, _, err := c.Collect(t.Context(), cursor1)
	require.NoError(t, err)
	require.Empty(t, events)
	require.NotEqual(t, "", cursorLastUpdated(t, cursor1))
}

func cursorLastUpdated(t *testing.T, cursor string) string {
	t.Helper()
	var st state
	require.NoError(t, json.Unmarshal([]byte(cursor), &st))
	return st.Cursor.LastUpdated
}
