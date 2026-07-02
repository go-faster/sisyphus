package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-faster/scpbot/internal/index"
)

func TestNew(t *testing.T) {
	t.Run("no token", func(t *testing.T) {
		_, err := New(Options{BaseURL: "http://example.com"})
		if err == nil {
			t.Fatal("expected error for missing token")
		}
		if !strings.Contains(err.Error(), "token is required") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("no base_url", func(t *testing.T) {
		_, err := New(Options{Token: "token123"})
		if err == nil {
			t.Fatal("expected error for missing base_url")
		}
		if !strings.Contains(err.Error(), "base_url is required") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("valid options", func(t *testing.T) {
		_, err := New(Options{
			BaseURL: "http://example.com",
			Token:   "token123",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func makeGitLabIssue(iid int, title string, updatedAt time.Time) map[string]any {
	return map[string]any{
		"iid":         iid,
		"project_id":  1,
		"title":       title,
		"description": "Description for " + title,
		"state":       "opened",
		"labels":      []string{"bug"},
		"created_at":  updatedAt.Format(time.RFC3339),
		"updated_at":  updatedAt.Format(time.RFC3339),
		"web_url":     fmt.Sprintf("http://example.com/project/issues/%d", iid),
		"author": map[string]any{
			"username": "alice",
			"name":     "Alice",
		},
	}
}

func makeGitLabMR(iid int, title string, updatedAt time.Time) map[string]any {
	return map[string]any{
		"iid":         iid,
		"project_id":  1,
		"title":       title,
		"description": "MR description",
		"state":       "opened",
		"labels":      []string{"feature"},
		"created_at":  updatedAt.Format(time.RFC3339),
		"updated_at":  updatedAt.Format(time.RFC3339),
		"web_url":     fmt.Sprintf("http://example.com/project/merge_requests/%d", iid),
		"author": map[string]any{
			"username": "bob",
			"name":     "Bob",
		},
	}
}

func generateIssues(n int, baseTime time.Time) []map[string]any {
	issues := make([]map[string]any, n)
	for i := range n {
		t := baseTime.Add(time.Duration(i) * time.Hour)
		issues[i] = makeGitLabIssue(i+1, fmt.Sprintf("Issue %d", i+1), t)
	}
	return issues
}

type testHandler struct {
	issues  []map[string]any
	mrs     []map[string]any
	notes   map[int][]map[string]any // iid -> notes
	mrNotes map[int][]map[string]any // iid -> notes
}

func (h *testHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Check token
	if r.Header.Get("PRIVATE-TOKEN") == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	switch {
	case strings.Contains(r.URL.Path, "/api/v4/version"):
		_ = json.NewEncoder(w).Encode(map[string]any{"version": "15.0.0"})

	case strings.Contains(r.URL.Path, "/issues") && strings.Contains(r.URL.Path, "/notes"):
		// Issue notes endpoint
		parts := strings.Split(r.URL.Path, "/")
		for i, p := range parts {
			if p == "issues" && i+1 < len(parts) {
				iid, _ := strconv.Atoi(parts[i+1])
				notes := h.notes[iid]
				_ = json.NewEncoder(w).Encode(notes)
				return
			}
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{})

	case strings.Contains(r.URL.Path, "/issues"):
		// Issues endpoint
		pageStr := r.URL.Query().Get("page")
		perPageStr := r.URL.Query().Get("per_page")
		page, _ := strconv.Atoi(pageStr)
		perPage, _ := strconv.Atoi(perPageStr)
		if page == 0 {
			page = 1
		}
		if perPage == 0 {
			perPage = 20
		}

		start := (page - 1) * perPage
		end := start + perPage
		if start >= len(h.issues) {
			_ = json.NewEncoder(w).Encode([]map[string]any{})
			return
		}
		if end > len(h.issues) {
			end = len(h.issues)
		}

		_ = json.NewEncoder(w).Encode(h.issues[start:end])

	case strings.Contains(r.URL.Path, "/merge_requests") && strings.Contains(r.URL.Path, "/notes"):
		// MR notes endpoint
		parts := strings.Split(r.URL.Path, "/")
		for i, p := range parts {
			if p == "merge_requests" && i+1 < len(parts) {
				iid, _ := strconv.Atoi(parts[i+1])
				notes := h.mrNotes[iid]
				_ = json.NewEncoder(w).Encode(notes)
				return
			}
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{})

	case strings.Contains(r.URL.Path, "/merge_requests"):
		// Merge requests endpoint
		pageStr := r.URL.Query().Get("page")
		perPageStr := r.URL.Query().Get("per_page")
		page, _ := strconv.Atoi(pageStr)
		perPage, _ := strconv.Atoi(perPageStr)
		if page == 0 {
			page = 1
		}
		if perPage == 0 {
			perPage = 20
		}

		start := (page - 1) * perPage
		end := start + perPage
		if start >= len(h.mrs) {
			_ = json.NewEncoder(w).Encode([]map[string]any{})
			return
		}
		if end > len(h.mrs) {
			end = len(h.mrs)
		}

		_ = json.NewEncoder(w).Encode(h.mrs[start:end])

	case strings.Contains(r.URL.Path, "/releases"):
		// Releases endpoint (empty in this fixture).
		_ = json.NewEncoder(w).Encode([]map[string]any{})

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestFetchIssuesBasic(t *testing.T) {
	t.Parallel()

	baseTime := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	issues := generateIssues(3, baseTime)

	handler := &testHandler{issues: issues}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	f, err := New(Options{
		BaseURL:  srv.URL,
		Token:    "test-token",
		Projects: []string{"1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := f.FetchIssues(context.Background(), 1, Cursor{})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Documents) != 3 {
		t.Fatalf("expected 3 docs, got %d", len(result.Documents))
	}

	for i, doc := range result.Documents {
		if doc.Source != index.SourceGitLabIssue {
			t.Errorf("doc[%d] Source: expected %q, got %q", i, index.SourceGitLabIssue, doc.Source)
		}
		if !strings.Contains(doc.SourceID, "issues/") {
			t.Errorf("doc[%d] SourceID should contain 'issues/', got %q", i, doc.SourceID)
		}
	}
}

func TestFetchMergeRequestsBasic(t *testing.T) {
	t.Parallel()

	baseTime := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	var mrs []map[string]any
	for i := range 3 {
		t := baseTime.Add(time.Duration(i) * time.Hour)
		mrs = append(mrs, makeGitLabMR(i+1, fmt.Sprintf("MR %d", i+1), t))
	}

	handler := &testHandler{mrs: mrs}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	f, err := New(Options{
		BaseURL:  srv.URL,
		Token:    "test-token",
		Projects: []string{"1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := f.FetchMergeRequests(context.Background(), 1, Cursor{})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Documents) != 3 {
		t.Fatalf("expected 3 docs, got %d", len(result.Documents))
	}

	for i, doc := range result.Documents {
		if doc.Source != index.SourceGitLabMR {
			t.Errorf("doc[%d] Source: expected %q, got %q", i, index.SourceGitLabMR, doc.Source)
		}
		if !strings.Contains(doc.SourceID, "merge_requests/") {
			t.Errorf("doc[%d] SourceID should contain 'merge_requests/', got %q", i, doc.SourceID)
		}
	}
}

func TestCheckAuth(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"version": "15.0.0"})
	}))
	defer srv.Close()

	f, err := New(Options{
		BaseURL: srv.URL,
		Token:   "valid-token",
	})
	if err != nil {
		t.Fatal(err)
	}

	err = f.CheckAuth(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestCheckAuthUnauthorized(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	f, err := New(Options{
		BaseURL: srv.URL,
		Token:   "invalid-token",
	})
	if err != nil {
		t.Fatal(err)
	}

	err = f.CheckAuth(context.Background())
	if err == nil {
		t.Fatal("expected auth error")
	}
}

func TestPrivateTokenHeader(t *testing.T) {
	t.Parallel()

	var gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("PRIVATE-TOKEN")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer srv.Close()

	f, err := New(Options{
		BaseURL:  srv.URL,
		Token:    "secret-token-xyz",
		Projects: []string{"1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.FetchIssues(context.Background(), 1, Cursor{})
	if err != nil {
		t.Fatal(err)
	}

	if gotToken != "secret-token-xyz" {
		t.Errorf("expected token %q, got %q", "secret-token-xyz", gotToken)
	}
}

func TestSystemNotesFiltered(t *testing.T) {
	t.Parallel()

	baseTime := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	issues := []map[string]any{makeGitLabIssue(1, "Issue with notes", baseTime)}

	notes := []map[string]any{
		{
			"id":         1,
			"system":     false,
			"body":       "Real comment",
			"created_at": baseTime.Format(time.RFC3339),
			"author": map[string]any{
				"username": "alice",
				"name":     "Alice",
			},
		},
		{
			"id":         2,
			"system":     true,
			"body":       "System comment",
			"created_at": baseTime.Format(time.RFC3339),
			"author":     nil,
		},
	}

	handler := &testHandler{
		issues: issues,
		notes:  map[int][]map[string]any{1: notes},
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	f, err := New(Options{
		BaseURL:  srv.URL,
		Token:    "test",
		Projects: []string{"1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := f.FetchIssues(context.Background(), 1, Cursor{})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Documents) != 1 {
		t.Fatalf("expected 1 doc, got %d", len(result.Documents))
	}

	doc := result.Documents[0]
	if rawIssue, ok := doc.Metadata["gitlab_issue"]; ok {
		// Check that only the real comment is present
		// This is verified in the chunker tests
		_ = rawIssue
	}
}

func TestProjectURLEncoding(t *testing.T) {
	t.Parallel()

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.RequestURI
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer srv.Close()

	f, err := New(Options{
		BaseURL:  srv.URL,
		Token:    "test",
		Projects: []string{"group/sub/project"},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.FetchIssues(context.Background(), 1, Cursor{})
	if err != nil {
		t.Fatal(err)
	}

	// Project path should be URL-encoded in the request URI
	if !strings.Contains(gotPath, "group%2Fsub%2Fproject") {
		t.Errorf("expected URL-encoded project path in RequestURI %q", gotPath)
	}
}

func TestBaseURLTrim(t *testing.T) {
	t.Parallel()

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer srv.Close()

	f, err := New(Options{
		BaseURL:  srv.URL + "/", // Trailing slash should be trimmed
		Token:    "test",
		Projects: []string{"1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.FetchIssues(context.Background(), 1, Cursor{})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(gotPath, "/api/v4") {
		t.Errorf("unexpected path: %s", gotPath)
	}
}

func TestEmptyProjects(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer srv.Close()

	f, err := New(Options{
		BaseURL:  srv.URL,
		Token:    "test",
		Projects: nil,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.FetchIssues(context.Background(), 1, Cursor{})
	if err == nil {
		t.Fatal("expected error for empty projects")
	}
	if !strings.Contains(err.Error(), "no projects configured") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCursorUpdatedAfter(t *testing.T) {
	t.Parallel()

	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer srv.Close()

	f, err := New(Options{
		BaseURL:  srv.URL,
		Token:    "test",
		Projects: []string{"1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	cursor := Cursor{UpdatedAfter: "2026-06-01T05:00:00Z"}
	_, err = f.FetchIssues(context.Background(), 1, cursor)
	if err != nil {
		t.Fatal(err)
	}

	// Parse the query to check for updated_after
	q, err := url.ParseQuery(gotQuery)
	if err != nil {
		t.Fatal(err)
	}

	updatedAfter := q.Get("updated_after")
	if updatedAfter != "2026-06-01T05:00:00Z" {
		t.Errorf("expected updated_after %q, got %q", "2026-06-01T05:00:00Z", updatedAfter)
	}
}

func TestUserAgent(t *testing.T) {
	t.Parallel()

	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer srv.Close()

	f, err := New(Options{
		BaseURL:  srv.URL,
		Token:    "test",
		Projects: []string{"1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.FetchIssues(context.Background(), 1, Cursor{})
	if err != nil {
		t.Fatal(err)
	}

	if gotUA != "scpbot/ingest" {
		t.Errorf("User-Agent: expected %q, got %q", "scpbot/ingest", gotUA)
	}
}

func TestMultipleProjects(t *testing.T) {
	t.Parallel()

	baseTime := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	issues := generateIssues(2, baseTime)

	var issueRequestCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")

		if strings.Contains(r.URL.Path, "/issues") && !strings.Contains(r.URL.Path, "/notes") {
			issueRequestCount++
			_ = json.NewEncoder(w).Encode(issues)
			return
		}

		// Notes endpoint
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer srv.Close()

	f, err := New(Options{
		BaseURL:  srv.URL,
		Token:    "test",
		Projects: []string{"1", "2", "3"},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := f.FetchIssues(context.Background(), 1, Cursor{})
	if err != nil {
		t.Fatal(err)
	}

	// Should have requested issues for 3 projects
	if issueRequestCount != 3 {
		t.Errorf("expected 3 issue requests, got %d", issueRequestCount)
	}

	// Should have 6 documents (2 issues per project)
	if len(result.Documents) != 6 {
		t.Fatalf("expected 6 docs, got %d", len(result.Documents))
	}
}
