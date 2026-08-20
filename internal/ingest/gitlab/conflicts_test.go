package gitlab

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func conflictMR(iid int, updated time.Time, fields map[string]any) map[string]any {
	mr := map[string]any{
		"iid":           iid,
		"project_id":    1,
		"title":         "Fix the thing",
		"state":         "opened",
		"created_at":    updated.Format(time.RFC3339),
		"updated_at":    updated.Format(time.RFC3339),
		"web_url":       "http://example.com/g/p/-/merge_requests/1",
		"sha":           "deadbeef",
		"source_branch": "feature",
		"target_branch": "main",
		"author":        map[string]any{"username": "alice", "name": "Alice"},
		"assignees":     []any{map[string]any{"username": "bob", "name": "Bob"}},
	}
	maps.Copy(mr, fields)
	return mr
}

func conflictFetcher(t *testing.T, pageSize int, handler http.HandlerFunc) *Fetcher {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	f, err := New(Options{
		BaseURL:  srv.URL,
		Token:    "token",
		Projects: []string{"g/p"},
		PageSize: pageSize,
	})
	require.NoError(t, err)
	return f
}

func TestFetchConflicts(t *testing.T) {
	updated := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	t.Run("only conflicting MRs", func(t *testing.T) {
		f := conflictFetcher(t, 10, func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode([]map[string]any{
				conflictMR(1, updated, map[string]any{"has_conflicts": true}),
				conflictMR(2, updated, map[string]any{"detailed_merge_status": "conflict"}),
				conflictMR(3, updated, map[string]any{"detailed_merge_status": "mergeable"}),
				// A pipeline that has not passed is not this event.
				conflictMR(4, updated, map[string]any{"detailed_merge_status": "ci_still_running"}),
				// An unsettled recheck is read as "not conflicting": the next
				// sweep sees the verdict.
				conflictMR(5, updated, map[string]any{"detailed_merge_status": "checking"}),
			})
		})

		refs, err := f.FetchConflicts(context.Background(), time.Time{})
		require.NoError(t, err)
		require.Len(t, refs, 2)
		require.Equal(t, 1, refs[0].MR.IID)
		require.Equal(t, 2, refs[1].MR.IID)
		require.Equal(t, "g/p", refs[0].Project)
		require.Equal(t, "alice", refs[0].MR.Author.Username)
		require.Equal(t, []string{"bob"}, refs[0].MR.Assignees)
		require.Equal(t, "deadbeef", refs[0].MR.SHA)
		require.Equal(t, "main", refs[0].MR.TargetBranch)
		require.Equal(t, updated, refs[0].MR.Updated)
	})

	t.Run("query", func(t *testing.T) {
		var got url.Values
		f := conflictFetcher(t, 10, func(w http.ResponseWriter, r *http.Request) {
			got = r.URL.Query()
			_, _ = w.Write([]byte("[]"))
		})

		since := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
		_, err := f.FetchConflicts(context.Background(), since)
		require.NoError(t, err)
		require.Equal(t, "opened", got.Get("state"))
		// Without the recheck GitLab may answer from a stale mergeability
		// verdict, which is the whole point of the sweep.
		require.Equal(t, "true", got.Get("with_merge_status_recheck"))
		require.Equal(t, since.Format(time.RFC3339), got.Get("updated_after"))
	})

	t.Run("paginates until short page", func(t *testing.T) {
		var pages []string
		f := conflictFetcher(t, 2, func(w http.ResponseWriter, r *http.Request) {
			page := r.URL.Query().Get("page")
			pages = append(pages, page)
			switch page {
			case "1":
				_ = json.NewEncoder(w).Encode([]map[string]any{
					conflictMR(1, updated, map[string]any{"has_conflicts": true}),
					conflictMR(2, updated, nil),
				})
			default:
				_ = json.NewEncoder(w).Encode([]map[string]any{
					conflictMR(3, updated, map[string]any{"has_conflicts": true}),
				})
			}
		})

		refs, err := f.FetchConflicts(context.Background(), time.Time{})
		require.NoError(t, err)
		require.Equal(t, []string{"1", "2"}, pages)
		require.Len(t, refs, 2)
	})

	t.Run("api error", func(t *testing.T) {
		f := conflictFetcher(t, 10, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})
		_, err := f.FetchConflicts(context.Background(), time.Time{})
		require.Error(t, err)
	})
}
