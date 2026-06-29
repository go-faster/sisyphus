package jira

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	chunkjira "github.com/go-faster/scpbot/internal/chunk/jira"
	"github.com/go-faster/scpbot/internal/index"
)

func TestNew(t *testing.T) {
	t.Run("no credentials", func(t *testing.T) {
		_, err := New(Options{BaseURL: "http://example.com"})
		if err == nil {
			t.Fatal("expected error for missing credentials")
		}
		if !strings.Contains(err.Error(), "no credentials") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("cloud credentials ok", func(t *testing.T) {
		_, err := New(Options{
			BaseURL:  "http://example.com",
			Email:    "user@example.com",
			APIToken: "token",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("username password credentials ok", func(t *testing.T) {
		_, err := New(Options{
			BaseURL:  "http://example.com",
			Username: "user",
			Password: "password",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("pat credentials ok", func(t *testing.T) {
		_, err := New(Options{
			BaseURL: "http://example.com",
			PAT:     "pat123",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func testJiraTime(t time.Time) string {
	return t.Format("2006-01-02T15:04:05.000-0700")
}

func makeIssue(id, key string, t time.Time) map[string]any {
	return map[string]any{
		"id":  id,
		"key": key,
		"fields": map[string]any{
			"summary":     "Issue " + key,
			"description": "Description for " + key,
			"status":      map[string]any{"name": "Done"},
			"resolution":  map[string]any{"name": "Fixed"},
			"created":     testJiraTime(t),
			"updated":     testJiraTime(t),
			"components":  []any{},
			"labels":      []string{"test"},
			"assignee":    map[string]any{"displayName": "John Doe"},
			"reporter":    map[string]any{"displayName": "Jane Doe"},
			"comment": map[string]any{
				"comments": []any{},
			},
		},
	}
}

func makeSearchResponse(startAt, maxResults, total int, issues []map[string]any) map[string]any {
	return map[string]any{
		"startAt":    startAt,
		"maxResults": maxResults,
		"total":      total,
		"issues":     issues,
	}
}

// generateIssues creates n issues with keys TEST-1 through TEST-n, each
// spaced by the given duration from baseTime.
func generateIssues(n int, baseTime time.Time) []map[string]any {
	issues := make([]map[string]any, n)
	for i := range n {
		t := baseTime.Add(time.Duration(i) * time.Hour)
		issues[i] = makeIssue(strconv.Itoa(i+1), fmt.Sprintf("TEST-%d", i+1), t)
	}
	return issues
}

// paginatedHandler returns a handler that slices from a fixed issue list.
func paginatedHandler(issues []map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		startAtStr := r.URL.Query().Get("startAt")
		maxResultsStr := r.URL.Query().Get("maxResults")
		startAt, _ := strconv.Atoi(startAtStr)
		maxResults, _ := strconv.Atoi(maxResultsStr)
		if maxResults <= 0 {
			maxResults = 100
		}

		end := min(startAt+maxResults, len(issues))
		page := issues[startAt:end]

		resp := makeSearchResponse(startAt, maxResults, len(issues), page)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

type testCase struct {
	name string
	run  func(t *testing.T)
}

func TestFetcher(t *testing.T) {
	cases := []testCase{
		{name: "SinglePagePartial", run: testSinglePagePartial},
		{name: "MultiPage", run: testMultiPage},
		{name: "CursorResume", run: testCursorResume},
		{name: "CloudAuth", run: testCloudAuth},
		{name: "UsernamePasswordAuth", run: testUsernamePasswordAuth},
		{name: "PAT", run: testPAT},
		{name: "ErrorPath", run: testErrorPath},
		{name: "EmptyProjects", run: testEmptyProjects},
	}
	for _, tc := range cases {
		t.Run(tc.name, tc.run)
	}
}

func testSinglePagePartial(t *testing.T) {
	t.Parallel()

	baseTime := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	issues := generateIssues(3, baseTime)
	srv := httptest.NewServer(paginatedHandler(issues))
	defer srv.Close()

	f, err := New(Options{
		BaseURL: srv.URL,
		PAT:     "test",
		Logger:  zap.NewNop(),
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := f.Fetch(context.Background(), FetchOptions{
		Projects: []string{"TEST"},
	}, Cursor{})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Documents) != 3 {
		t.Fatalf("expected 3 docs, got %d", len(result.Documents))
	}

	lastUpdated := baseTime.Add(2 * time.Hour)
	expectedLastStr := lastUpdated.Format(time.RFC3339)
	if result.NextCursor.LastUpdated != expectedLastStr {
		t.Errorf("expected LastUpdated %q, got %q", expectedLastStr, result.NextCursor.LastUpdated)
	}
	if result.NextCursor.StartAt != 0 {
		t.Errorf("expected StartAt=0, got %d", result.NextCursor.StartAt)
	}
	if result.HasMore {
		t.Error("expected HasMore=false")
	}

	// Verify document content
	for i, d := range result.Documents {
		if d.Source != index.SourceJira {
			t.Errorf("doc[%d] Source: expected %q, got %q", i, index.SourceJira, d.Source)
		}
		expectedKey := fmt.Sprintf("TEST-%d", i+1)
		if d.SourceID != expectedKey {
			t.Errorf("doc[%d] SourceID: expected %q, got %q", i, expectedKey, d.SourceID)
		}
		if d.Title != "Issue "+expectedKey {
			t.Errorf("doc[%d] Title: expected %q, got %q", i, "Issue "+expectedKey, d.Title)
		}
	}
}

func testMultiPage(t *testing.T) {
	t.Parallel()

	// 205 issues: two full pages of 100, then a partial page of 5
	baseTime := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	issues := generateIssues(205, baseTime)
	srv := httptest.NewServer(paginatedHandler(issues))
	defer srv.Close()

	f, err := New(Options{
		BaseURL:  srv.URL,
		PAT:      "test",
		Logger:   zap.NewNop(),
		PageSize: 100,
	})
	if err != nil {
		t.Fatal(err)
	}

	var batches [][]index.Document
	_, err = f.FetchAll(context.Background(), FetchOptions{
		Projects: []string{"TEST"},
	}, Cursor{}, func(_ context.Context, docs []index.Document, _ Cursor) error {
		batches = append(batches, docs)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(batches) != 3 {
		t.Fatalf("expected 3 batches, got %d", len(batches))
	}

	if len(batches[0]) != 100 {
		t.Errorf("batch[0] length: expected 100, got %d", len(batches[0]))
	}
	if len(batches[1]) != 100 {
		t.Errorf("batch[1] length: expected 100, got %d", len(batches[1]))
	}
	if len(batches[2]) != 5 {
		t.Errorf("batch[2] length: expected 5, got %d", len(batches[2]))
	}

	// Verify documents span the full range
	checkKeys := func(t *testing.T, docs []index.Document, startKey int) {
		t.Helper()
		for i, d := range docs {
			expectedKey := fmt.Sprintf("TEST-%d", startKey+i)
			if d.SourceID != expectedKey {
				t.Errorf("doc[%d] SourceID: expected %q, got %q", i, expectedKey, d.SourceID)
			}
		}
	}
	checkKeys(t, batches[0], 1)
	checkKeys(t, batches[1], 101)
	checkKeys(t, batches[2], 201)
}

func testCursorResume(t *testing.T) {
	t.Parallel()

	baseTime := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	issues := generateIssues(250, baseTime)
	var gotJQL string
	var gotStartAt string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotJQL = r.URL.Query().Get("jql")
		gotStartAt = r.URL.Query().Get("startAt")

		startAtStr := r.URL.Query().Get("startAt")
		maxResultsStr := r.URL.Query().Get("maxResults")
		startAt, _ := strconv.Atoi(startAtStr)
		maxResults, _ := strconv.Atoi(maxResultsStr)
		if maxResults <= 0 {
			maxResults = 100
		}

		end := min(startAt+maxResults, len(issues))
		page := issues[startAt:end]
		resp := makeSearchResponse(startAt, maxResults, len(issues), page)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	f, err := New(Options{
		BaseURL: srv.URL,
		PAT:     "test",
		Logger:  zap.NewNop(),
	})
	if err != nil {
		t.Fatal(err)
	}

	cursor := Cursor{
		LastUpdated: "2026-06-01T05:00:00Z",
		StartAt:     100,
	}

	result, err := f.Fetch(context.Background(), FetchOptions{
		Projects: []string{"TEST"},
	}, cursor)
	if err != nil {
		t.Fatal(err)
	}

	// Verify the JQL contains the expected updated clause
	if !strings.Contains(gotJQL, `updated >= "2026-06-01T05:00:00Z"`) {
		t.Errorf("JQL missing updated clause: %s", gotJQL)
	}
	if !strings.Contains(gotJQL, "ORDER BY updated ASC") {
		t.Errorf("JQL missing ORDER BY: %s", gotJQL)
	}

	// Verify startAt was sent correctly
	if gotStartAt != "100" {
		t.Errorf("startAt: expected 100, got %s", gotStartAt)
	}

	// Should return 100 issues (100-199)
	if len(result.Documents) != 100 {
		t.Fatalf("expected 100 docs, got %d", len(result.Documents))
	}

	// Next cursor should have advanced
	if result.NextCursor.StartAt != 200 {
		t.Errorf("NextCursor.StartAt: expected 200, got %d", result.NextCursor.StartAt)
	}
	if result.NextCursor.LastUpdated != "2026-06-01T05:00:00Z" {
		t.Errorf("NextCursor.LastUpdated should be unchanged: got %s", result.NextCursor.LastUpdated)
	}
}

func testCloudAuth(t *testing.T) {
	t.Parallel()

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")

		resp := makeSearchResponse(0, 100, 0, []map[string]any{})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	email := "user@example.com"
	token := "api-token-123"
	expectedAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(email+":"+token))

	f, err := New(Options{
		BaseURL:  srv.URL,
		Email:    email,
		APIToken: token,
		Logger:   zap.NewNop(),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.Fetch(context.Background(), FetchOptions{
		Projects: []string{"TEST"},
	}, Cursor{})
	if err != nil {
		t.Fatal(err)
	}

	if gotAuth != expectedAuth {
		t.Errorf("Authorization: expected %q, got %q", expectedAuth, gotAuth)
	}
}

func testPAT(t *testing.T) {
	t.Parallel()

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")

		resp := makeSearchResponse(0, 100, 0, []map[string]any{})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	f, err := New(Options{
		BaseURL: srv.URL,
		PAT:     "pat-secret-456",
		Logger:  zap.NewNop(),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.Fetch(context.Background(), FetchOptions{
		Projects: []string{"TEST"},
	}, Cursor{})
	if err != nil {
		t.Fatal(err)
	}

	expectedAuth := "Bearer pat-secret-456"
	if gotAuth != expectedAuth {
		t.Errorf("Authorization: expected %q, got %q", expectedAuth, gotAuth)
	}
}

func testUsernamePasswordAuth(t *testing.T) {
	t.Parallel()

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")

		resp := makeSearchResponse(0, 100, 0, []map[string]any{})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	username := "jira-user"
	password := "jira-password"
	expectedAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))

	f, err := New(Options{
		BaseURL:  srv.URL,
		Username: username,
		Password: password,
		Logger:   zap.NewNop(),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.Fetch(context.Background(), FetchOptions{
		Projects: []string{"TEST"},
	}, Cursor{})
	if err != nil {
		t.Fatal(err)
	}

	if gotAuth != expectedAuth {
		t.Errorf("Authorization: expected %q, got %q", expectedAuth, gotAuth)
	}
}

func testErrorPath(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errorMessages": []string{"Unauthorized"},
		})
	}))
	defer srv.Close()

	f, err := New(Options{
		BaseURL: srv.URL,
		PAT:     "test",
		Logger:  zap.NewNop(),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.Fetch(context.Background(), FetchOptions{
		Projects: []string{"TEST"},
	}, Cursor{})
	if err == nil {
		t.Fatal("expected error from 401 response")
	}
	if !strings.Contains(err.Error(), "jira search status 401") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func testEmptyProjects(t *testing.T) {
	t.Parallel()

	f, err := New(Options{
		BaseURL: "http://example.com",
		PAT:     "test",
		Logger:  zap.NewNop(),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.Fetch(context.Background(), FetchOptions{}, Cursor{})
	if err == nil {
		t.Fatal("expected error for empty projects")
	}
	if !strings.Contains(err.Error(), "empty projects") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestIssueMapping(t *testing.T) {
	t.Parallel()

	baseTime := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	issues := []map[string]any{
		{
			"id":  "101",
			"key": "BILL-42",
			"fields": map[string]any{
				"summary":     "Fix the thing",
				"description": "Something is broken",
				"status":      map[string]any{"name": "In Progress"},
				"resolution":  nil,
				"created":     testJiraTime(baseTime),
				"updated":     testJiraTime(baseTime.Add(time.Hour)),
				"components": []any{
					map[string]any{"name": "Backend"},
				},
				"labels": []string{"bug", "critical"},
				"assignee": map[string]any{
					"displayName": "Alice",
				},
				"reporter": map[string]any{
					"displayName": "Bob",
				},
				"comment": map[string]any{
					"comments": []any{
						map[string]any{
							"author":  map[string]any{"displayName": "Charlie"},
							"body":    "Looking into it",
							"created": testJiraTime(baseTime.Add(30 * time.Minute)),
							"updated": testJiraTime(baseTime.Add(30 * time.Minute)),
						},
					},
				},
			},
		},
	}

	srv := httptest.NewServer(paginatedHandler(issues))
	defer srv.Close()

	f, err := New(Options{
		BaseURL: srv.URL,
		PAT:     "test",
		Logger:  zap.NewNop(),
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := f.Fetch(context.Background(), FetchOptions{
		Projects: []string{"BILL"},
	}, Cursor{})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Documents) != 1 {
		t.Fatalf("expected 1 document, got %d", len(result.Documents))
	}

	doc := result.Documents[0]
	if doc.Source != index.SourceJira {
		t.Errorf("Source: expected %q, got %q", index.SourceJira, doc.Source)
	}
	if doc.SourceID != "BILL-42" {
		t.Errorf("SourceID: expected BILL-42, got %q", doc.SourceID)
	}
	if doc.Title != "Fix the thing" {
		t.Errorf("Title: expected %q, got %q", "Fix the thing", doc.Title)
	}

	issRaw, ok := doc.Metadata["jira_issue"]
	if !ok {
		t.Fatal("metadata missing jira_issue")
	}
	iss, ok := issRaw.(chunkjira.Issue)
	if !ok {
		t.Fatalf("jira_issue has wrong type %T", issRaw)
	}

	if iss.Key != "BILL-42" {
		t.Errorf("Issue.Key: expected BILL-42, got %q", iss.Key)
	}
	if iss.Title != "Fix the thing" {
		t.Errorf("Issue.Title: expected %q, got %q", "Fix the thing", iss.Title)
	}
	if iss.Description != "Something is broken" {
		t.Errorf("Issue.Description: expected %q, got %q", "Something is broken", iss.Description)
	}
	if iss.Status != "In Progress" {
		t.Errorf("Issue.Status: expected %q, got %q", "In Progress", iss.Status)
	}
	if iss.Resolution != "" {
		t.Errorf("Issue.Resolution: expected empty (nil), got %q", iss.Resolution)
	}
	if len(iss.Components) != 1 || iss.Components[0] != "Backend" {
		t.Errorf("Issue.Components: expected [Backend], got %v", iss.Components)
	}
	if len(iss.Labels) != 2 {
		t.Errorf("Issue.Labels: expected 2 labels, got %v", iss.Labels)
	}
	if iss.Assignee != "Alice" {
		t.Errorf("Issue.Assignee: expected Alice, got %q", iss.Assignee)
	}
	if iss.Reporter != "Bob" {
		t.Errorf("Issue.Reporter: expected Bob, got %q", iss.Reporter)
	}
	if len(iss.Comments) != 1 {
		t.Fatalf("Issue.Comments: expected 1 comment, got %d", len(iss.Comments))
	}
	if iss.Comments[0].Author != "Charlie" {
		t.Errorf("Comment[0].Author: expected Charlie, got %q", iss.Comments[0].Author)
	}
	if iss.Comments[0].Body != "Looking into it" {
		t.Errorf("Comment[0].Body: expected %q, got %q", "Looking into it", iss.Comments[0].Body)
	}
	if !iss.Created.Equal(baseTime) {
		t.Errorf("Issue.Created: expected %v, got %v", baseTime, iss.Created)
	}
	if !iss.Updated.Equal(baseTime.Add(time.Hour)) {
		t.Errorf("Issue.Updated: expected %v, got %v", baseTime.Add(time.Hour), iss.Updated)
	}
}

func TestBaseURLTrim(t *testing.T) {
	t.Parallel()

	var capturedURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedURL = r.URL.String()
		resp := makeSearchResponse(0, 100, 0, []map[string]any{})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	f, err := New(Options{
		BaseURL: srv.URL + "/", // trailing slash should be trimmed
		PAT:     "test",
		Logger:  zap.NewNop(),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.Fetch(context.Background(), FetchOptions{
		Projects: []string{"TEST"},
	}, Cursor{})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(capturedURL, "/rest/api/2/search?") {
		t.Errorf("unexpected request path: %s", capturedURL)
	}
}

func TestFetchAllFinalCursor(t *testing.T) {
	t.Parallel()

	issues := generateIssues(50, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	srv := httptest.NewServer(paginatedHandler(issues))
	defer srv.Close()

	f, err := New(Options{
		BaseURL: srv.URL,
		PAT:     "test",
		Logger:  zap.NewNop(),
	})
	if err != nil {
		t.Fatal(err)
	}

	var lastCursor Cursor
	finalCursor, err := f.FetchAll(context.Background(), FetchOptions{
		Projects: []string{"TEST"},
	}, Cursor{}, func(_ context.Context, docs []index.Document, cursor Cursor) error {
		lastCursor = cursor
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Final cursor from the return value
	if finalCursor.StartAt != 0 {
		t.Errorf("final StartAt: expected 0, got %d", finalCursor.StartAt)
	}
	if finalCursor.LastUpdated == "" {
		t.Error("final LastUpdated should not be empty")
	}

	// Both cursors should match (last batch's cursor = final cursor)
	if lastCursor != finalCursor {
		t.Errorf("lastCursor %+v != finalCursor %+v", lastCursor, finalCursor)
	}
}

func TestUpdatedAfterZero(t *testing.T) {
	t.Parallel()

	var gotJQL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotJQL = r.URL.Query().Get("jql")
		resp := makeSearchResponse(0, 100, 0, []map[string]any{})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	f, err := New(Options{
		BaseURL: srv.URL,
		PAT:     "test",
		Logger:  zap.NewNop(),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.Fetch(context.Background(), FetchOptions{
		Projects: []string{"TEST"},
		// UpdatedAfter is zero — no updated >= clause
	}, Cursor{})
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(gotJQL, "updated >=") {
		t.Errorf("JQL should not contain updated >= when UpdatedAfter is zero: %s", gotJQL)
	}
}

func TestUserAgent(t *testing.T) {
	t.Parallel()

	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		resp := makeSearchResponse(0, 100, 0, []map[string]any{})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	f, err := New(Options{
		BaseURL: srv.URL,
		PAT:     "test",
		Logger:  zap.NewNop(),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.Fetch(context.Background(), FetchOptions{
		Projects: []string{"TEST"},
	}, Cursor{})
	if err != nil {
		t.Fatal(err)
	}

	if gotUA != "scpbot/ingest" {
		t.Errorf("User-Agent: expected %q, got %q", "scpbot/ingest", gotUA)
	}
}

func TestJQLURLEncoding(t *testing.T) {
	t.Parallel()

	var rawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		resp := makeSearchResponse(0, 100, 0, []map[string]any{})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	f, err := New(Options{
		BaseURL: srv.URL,
		PAT:     "test",
		Logger:  zap.NewNop(),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.Fetch(context.Background(), FetchOptions{
		Projects: []string{"BILL"},
	}, Cursor{
		LastUpdated: "2026-06-01T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify JQL is URL-encoded
	if !strings.Contains(rawQuery, "jql=") {
		t.Error("missing jql parameter in query string")
	}
	parsed, err := url.ParseQuery(rawQuery)
	if err != nil {
		t.Fatal(err)
	}
	jql := parsed.Get("jql")
	if !strings.Contains(jql, `project IN ("BILL")`) {
		t.Errorf("unexpected JQL: %s", jql)
	}
	if !strings.Contains(jql, `updated >= "2026-06-01T00:00:00Z"`) {
		t.Errorf("missing updated clause in JQL: %s", jql)
	}
}

func TestBadTimeSkipsIssue(t *testing.T) {
	t.Parallel()

	baseTime := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	issues := []map[string]any{
		{
			"id":  "1",
			"key": "TEST-1",
			"fields": map[string]any{
				"summary":     "Good issue",
				"description": "Has valid times",
				"created":     testJiraTime(baseTime),
				"updated":     testJiraTime(baseTime),
				"components":  []any{},
				"labels":      []string{},
			},
		},
		{
			"id":  "2",
			"key": "TEST-2",
			"fields": map[string]any{
				"summary":     "Bad issue",
				"description": "Has invalid updated time",
				"created":     testJiraTime(baseTime),
				"updated":     "not-a-date",
				"components":  []any{},
				"labels":      []string{},
			},
		},
	}

	srv := httptest.NewServer(paginatedHandler(issues))
	defer srv.Close()

	f, err := New(Options{
		BaseURL: srv.URL,
		PAT:     "test",
		Logger:  zap.NewNop(),
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := f.Fetch(context.Background(), FetchOptions{
		Projects: []string{"TEST"},
	}, Cursor{})
	if err != nil {
		t.Fatal(err)
	}

	// The bad issue should be skipped, we should only have TEST-1
	if len(result.Documents) != 1 {
		t.Fatalf("expected 1 document (bad issue skipped), got %d", len(result.Documents))
	}
	if result.Documents[0].SourceID != "TEST-1" {
		t.Errorf("expected TEST-1, got %s", result.Documents[0].SourceID)
	}
}
