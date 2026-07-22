package jira

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	chunkjira "github.com/go-faster/sisyphus/internal/chunk/jira"
	ingestjira "github.com/go-faster/sisyphus/internal/ingest/jira"
	"github.com/go-faster/sisyphus/internal/notify"
)

// fakeFetcher serves canned pages keyed by the incoming cursor's
// LastUpdated, so a test can simulate the collector re-polling with the
// cursor it advanced to on the previous call.
type fakeFetcher struct {
	pages map[string][]chunkjira.Issue
}

func (f *fakeFetcher) FetchIssuesStructured(_ context.Context, _ ingestjira.FetchOptions, cursor ingestjira.Cursor) ([]chunkjira.Issue, ingestjira.Cursor, bool, error) {
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

func TestCollector_EmitsIssueAssigned(t *testing.T) {
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
	require.Equal(t, notify.EventIssueAssigned, e.Type)
	require.Equal(t, "acc-alice", e.Recipient.Key)
	require.Equal(t, "Alice", e.Recipient.Display)
	require.Equal(t, "Rachel", e.Actor.Display)
	require.Equal(t, "jira_assign:IDP-1:acc-alice", e.EventID)
}

func TestCollector_ReRunWithAdvancedCursorEmitsNothingNew(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	unchanged := issue("acc-alice", "Alice", t1)
	fetcher := &fakeFetcher{pages: map[string][]chunkjira.Issue{
		"": {unchanged},
	}}
	c := New(fetcher, []string{"IDP"})

	_, cursor1, err := c.Collect(t.Context(), "")
	require.NoError(t, err)

	fetcher.pages[cursorLastUpdated(t, cursor1)] = []chunkjira.Issue{unchanged}
	events, _, err := c.Collect(t.Context(), cursor1)
	require.NoError(t, err)
	require.Empty(t, events)
}

func TestCollector_ReassignmentFiresNewEvent(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	fetcher := &fakeFetcher{pages: map[string][]chunkjira.Issue{
		"": {issue("acc-alice", "Alice", t1)},
	}}
	c := New(fetcher, []string{"IDP"})

	_, cursor1, err := c.Collect(t.Context(), "")
	require.NoError(t, err)

	fetcher.pages[cursorLastUpdated(t, cursor1)] = []chunkjira.Issue{
		issue("acc-dave", "Dave", t2),
	}
	events, _, err := c.Collect(t.Context(), cursor1)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, "acc-dave", events[0].Recipient.Key)
}

func TestCollector_UnassignedIssueEmitsNothing(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	fetcher := &fakeFetcher{pages: map[string][]chunkjira.Issue{
		"": {issue("", "", t1)},
	}}
	c := New(fetcher, []string{"IDP"})

	events, _, err := c.Collect(t.Context(), "")
	require.NoError(t, err)
	require.Empty(t, events)
}

func cursorLastUpdated(t *testing.T, cursor string) string {
	t.Helper()
	var st state
	require.NoError(t, json.Unmarshal([]byte(cursor), &st))
	return st.Cursor.LastUpdated
}
