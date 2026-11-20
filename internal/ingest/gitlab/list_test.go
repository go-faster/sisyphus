package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// listHandler serves a paginated list of objects, recording the queries it was
// asked, so a test can assert both the results and how they were requested.
type listHandler struct {
	items    []map[string]any
	pageSize int
	queries  []string
}

func (h *listHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.queries = append(h.queries, r.URL.RawQuery)

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	start := (page - 1) * h.pageSize
	end := min(start+h.pageSize, len(h.items))
	if start > len(h.items) {
		start = len(h.items)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(h.items[start:end])
}

func issueItems(n int) []map[string]any {
	out := make([]map[string]any, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, map[string]any{"iid": i, "id": 1000 + i})
	}
	return out
}

func newListFetcher(t *testing.T, h *listHandler, pageSize int) *Fetcher {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	f, err := New(Options{
		BaseURL:  srv.URL,
		Token:    "test-token",
		Projects: []string{"grp/one"},
		PageSize: pageSize,
	})
	require.NoError(t, err)
	return f
}

// TestListSourceIDsMatchesDocumentIDs pins the shape a reconcile compares
// against. If these drift from internal/chunk/gitlab's source ids, a reconcile
// finds every document missing and deletes the lot.
func TestListSourceIDsMatchesDocumentIDs(t *testing.T) {
	h := &listHandler{items: issueItems(3), pageSize: 100}
	f := newListFetcher(t, h, 100)

	got, err := f.ListSourceIDs(context.Background(), "grp/one", ResourceIssues)
	require.NoError(t, err)
	require.Equal(t, []string{"grp/one/issues/1", "grp/one/issues/2", "grp/one/issues/3"}, got)
}

// TestListSourceIDsAsksForEveryState pins that the listing is not filtered to
// open objects: a closed issue is still an issue, and one absent from the
// listing is one a reconcile deletes.
func TestListSourceIDsAsksForEveryState(t *testing.T) {
	h := &listHandler{items: issueItems(1), pageSize: 100}
	f := newListFetcher(t, h, 100)

	_, err := f.ListSourceIDs(context.Background(), "grp/one", ResourceIssues)
	require.NoError(t, err)
	require.Contains(t, h.queries[0], "state=all")
	// Ordering by id keeps the walk stable while objects are edited under it.
	require.Contains(t, h.queries[0], "order_by=id")
}

// TestListSourceIDsPaginates pins that a listing spanning pages returns all of
// it — a truncated listing reads downstream as a mass deletion.
func TestListSourceIDsPaginates(t *testing.T) {
	h := &listHandler{items: issueItems(25), pageSize: 10}
	f := newListFetcher(t, h, 10)

	got, err := f.ListSourceIDs(context.Background(), "grp/one", ResourceIssues)
	require.NoError(t, err)
	require.Len(t, got, 25)
	require.Equal(t, "grp/one/issues/25", got[24])
	require.Len(t, h.queries, 3, "two full pages plus the short one that ends it")
}

// TestListSourceIDsExactPageBoundary pins the off-by-one: when the last page is
// exactly full, the walk must ask once more rather than assume it is done.
func TestListSourceIDsExactPageBoundary(t *testing.T) {
	h := &listHandler{items: issueItems(20), pageSize: 10}
	f := newListFetcher(t, h, 10)

	got, err := f.ListSourceIDs(context.Background(), "grp/one", ResourceIssues)
	require.NoError(t, err)
	require.Len(t, got, 20)
	require.Len(t, h.queries, 3, "the empty third page is what proves the end")
}

func TestListSourceIDsReleasesUseTagNames(t *testing.T) {
	h := &listHandler{items: []map[string]any{
		{"tag_name": "v1.0.0"},
		{"tag_name": "v1.1.0"},
	}, pageSize: 100}
	f := newListFetcher(t, h, 100)

	got, err := f.ListSourceIDs(context.Background(), "grp/one", ResourceReleases)
	require.NoError(t, err)
	require.Equal(t, []string{"grp/one/releases/v1.0.0", "grp/one/releases/v1.1.0"}, got)
}

func TestListSourceIDsMergeRequests(t *testing.T) {
	h := &listHandler{items: issueItems(2), pageSize: 100}
	f := newListFetcher(t, h, 100)

	got, err := f.ListSourceIDs(context.Background(), "grp/one", ResourceMergeRequests)
	require.NoError(t, err)
	require.Equal(t, []string{"grp/one/merge_requests/1", "grp/one/merge_requests/2"}, got)
}

func TestListSourceIDsRejectsUnknownKind(t *testing.T) {
	h := &listHandler{items: nil, pageSize: 100}
	f := newListFetcher(t, h, 100)

	_, err := f.ListSourceIDs(context.Background(), "grp/one", ResourceKind("wikis"))
	require.Error(t, err)
}

// TestListSourceIDsFailsOnServerError pins that an upstream failure surfaces as
// an error rather than a short list.
func TestListSourceIDsFailsOnServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	f, err := New(Options{BaseURL: srv.URL, Token: "t", Projects: []string{"grp/one"}})
	require.NoError(t, err)

	_, err = f.ListSourceIDs(context.Background(), "grp/one", ResourceIssues)
	require.Error(t, err)
}
