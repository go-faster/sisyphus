package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// jiraListHandler serves a paginated JQL search, recording each query.
type jiraListHandler struct {
	total    int
	pageSize int
	queries  []string
}

func (h *jiraListHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.queries = append(h.queries, r.URL.RawQuery)

	startAt, _ := strconv.Atoi(r.URL.Query().Get("startAt"))
	end := min(startAt+h.pageSize, h.total)

	issues := make([]map[string]any, 0, max(0, end-startAt))
	for i := startAt; i < end; i++ {
		issues = append(issues, map[string]any{
			"id":  strconv.Itoa(i),
			"key": fmt.Sprintf("ABC-%d", i+1),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"startAt": startAt,
		"total":   h.total,
		"issues":  issues,
	})
}

func newJiraListFetcher(t *testing.T, h *jiraListHandler) *Fetcher {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	f, err := New(Options{
		BaseURL:  srv.URL,
		PAT:      "test-token",
		PageSize: h.pageSize,
	})
	require.NoError(t, err)
	return f
}

// TestListSourceIDsReturnsKeys pins the shape a reconcile diffs against: a Jira
// document's source id is its issue key.
func TestListSourceIDsReturnsKeys(t *testing.T) {
	h := &jiraListHandler{total: 3, pageSize: 50}
	f := newJiraListFetcher(t, h)

	got, err := f.ListSourceIDs(context.Background(), "ABC")
	require.NoError(t, err)
	require.Equal(t, []string{"ABC-1", "ABC-2", "ABC-3"}, got)
}

// TestListSourceIDsAsksOnlyForKeys pins that a listing does not pull *all — the
// whole point of a separate lister is that it is cheap enough to run over a
// whole project.
func TestListSourceIDsAsksOnlyForKeys(t *testing.T) {
	h := &jiraListHandler{total: 1, pageSize: 50}
	f := newJiraListFetcher(t, h)

	_, err := f.ListSourceIDs(context.Background(), "ABC")
	require.NoError(t, err)
	require.Contains(t, h.queries[0], "fields=key")
	require.NotContains(t, h.queries[0], "%2Aall")
	// No `updated` bound: this is the whole project, not a window.
	require.NotContains(t, h.queries[0], "updated+%3E%3D")
}

func TestListSourceIDsPaginates(t *testing.T) {
	h := &jiraListHandler{total: 25, pageSize: 10}
	f := newJiraListFetcher(t, h)

	got, err := f.ListSourceIDs(context.Background(), "ABC")
	require.NoError(t, err)
	require.Len(t, got, 25)
	require.Equal(t, "ABC-25", got[24])
	require.Len(t, h.queries, 3)
}

func TestListSourceIDsRejectsEmptyProject(t *testing.T) {
	h := &jiraListHandler{total: 0, pageSize: 10}
	f := newJiraListFetcher(t, h)

	_, err := f.ListSourceIDs(context.Background(), "  ")
	require.Error(t, err)
}

// TestListSourceIDsFailsOnServerError pins that an upstream failure surfaces as
// an error, never as a short list a reconcile would read as deletions.
func TestListSourceIDsFailsOnServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	f, err := New(Options{BaseURL: srv.URL, PAT: "t"})
	require.NoError(t, err)

	_, err = f.ListSourceIDs(context.Background(), "ABC")
	require.Error(t, err)
}
