package jira

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// cursorAt is the incremental bound under test: 2026-07-17T07:33:35Z.
const cursorAt = "2026-07-17T07:33:35Z"

func TestBuildJQLRendersBoundInUserTimezone(t *testing.T) {
	tests := []struct {
		name string
		zone string
		want string
	}{
		{
			// East of UTC: rendering the UTC clock understated the bound by the
			// offset. Harmless (the window only widened) but still wrong.
			name: "east of UTC",
			zone: "Asia/Tashkent", // UTC+5
			want: `updated >= "2026-07-17 12:33"`,
		},
		{
			// West of UTC is the data-loss case: rendering the UTC clock put the
			// bound 5h *ahead* of the cursor, so everything updated in between
			// was skipped and never looked at again.
			name: "west of UTC",
			zone: "America/New_York", // UTC-4 in July
			want: `updated >= "2026-07-17 03:33"`,
		},
		{
			name: "UTC",
			zone: "UTC",
			want: `updated >= "2026-07-17 07:33"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loc, err := time.LoadLocation(tt.zone)
			if err != nil {
				t.Fatalf("load %s: %v", tt.zone, err)
			}
			got := buildJQL([]string{"ABC"}, Cursor{LastUpdated: cursorAt}, FetchOptions{}, loc)
			if !strings.Contains(got, tt.want) {
				t.Errorf("jql = %q, want it to contain %q", got, tt.want)
			}
		})
	}
}

// TestBuildJQLNeverMovesBoundForward is the invariant that matters regardless of
// zone: the rendered bound, read back in the user's zone, must never be later
// than the cursor, or issues updated in the gap are skipped silently.
func TestBuildJQLNeverMovesBoundForward(t *testing.T) {
	cursor, err := time.Parse(time.RFC3339, cursorAt)
	if err != nil {
		t.Fatal(err)
	}
	for _, zone := range []string{
		"Asia/Tashkent",      // UTC+5
		"Pacific/Kiritimati", // UTC+14, the eastern extreme
		"Etc/GMT+12",         // UTC-12, the western extreme
		"America/New_York",
		"Europe/Moscow",
		"UTC",
	} {
		t.Run(zone, func(t *testing.T) {
			loc, err := time.LoadLocation(zone)
			if err != nil {
				t.Fatalf("load %s: %v", zone, err)
			}
			jql := buildJQL([]string{"ABC"}, Cursor{LastUpdated: cursorAt}, FetchOptions{}, loc)

			_, literal, ok := strings.Cut(jql, `updated >= "`)
			if !ok {
				t.Fatalf("no bound in %q", jql)
			}
			literal, _, ok = strings.Cut(literal, `"`)
			if !ok {
				t.Fatalf("unterminated bound in %q", jql)
			}
			// Jira parses the literal in the user's zone; do the same to recover
			// the instant it actually selects from.
			bound, err := time.ParseInLocation(jqlDateFormat, literal, loc)
			if err != nil {
				t.Fatalf("parse bound %q: %v", literal, err)
			}
			if bound.After(cursor) {
				t.Errorf("bound %s is after cursor %s: issues updated in between are skipped",
					bound, cursor)
			}
			// Minute truncation is the only slack allowed.
			if cursor.Sub(bound) >= time.Minute {
				t.Errorf("bound %s trails cursor %s by %s, want < 1m",
					bound, cursor, cursor.Sub(bound))
			}
		})
	}
}

func TestBuildJQLNilLocationDefaultsUTC(t *testing.T) {
	got := buildJQL([]string{"ABC"}, Cursor{LastUpdated: cursorAt}, FetchOptions{}, nil)
	if want := `updated >= "2026-07-17 07:33"`; !strings.Contains(got, want) {
		t.Errorf("jql = %q, want it to contain %q", got, want)
	}
}

// TestLocationFromMyself covers the wiring: the timezone comes off /myself, is
// resolved once, and falls back to UTC rather than failing the run.
func TestLocationFromMyself(t *testing.T) {
	tests := []struct {
		name     string
		timeZone string
		status   int
		want     string
	}{
		{name: "resolves reported zone", timeZone: "Asia/Tashkent", status: http.StatusOK, want: "Asia/Tashkent"},
		{name: "unknown zone falls back", timeZone: "Mars/Olympus", status: http.StatusOK, want: "UTC"},
		{name: "empty zone falls back", timeZone: "", status: http.StatusOK, want: "UTC"},
		// 403 rather than 5xx: netclient retries 5xx with real backoff, and a
		// non-retried status exercises the same fallback without the sleeps.
		{name: "lookup failure falls back", timeZone: "Asia/Tashkent", status: http.StatusForbidden, want: "UTC"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/myself") {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				calls++
				if tt.status != http.StatusOK {
					w.WriteHeader(tt.status)
					return
				}
				_ = json.NewEncoder(w).Encode(jiraUserResponse{TimeZone: tt.timeZone})
			}))
			defer srv.Close()

			f, err := New(Options{BaseURL: srv.URL, PAT: "token", HTTPClient: srv.Client()})
			if err != nil {
				t.Fatal(err)
			}
			if got := f.location(t.Context()).String(); got != tt.want {
				t.Errorf("location = %q, want %q", got, tt.want)
			}
			// Resolved once per Fetcher, not per Fetch.
			if got := f.location(t.Context()).String(); got != tt.want {
				t.Errorf("second location = %q, want %q", got, tt.want)
			}
			if calls != 1 {
				t.Errorf("/myself called %d times, want 1", calls)
			}
		})
	}
}

// TestCheckAuthSeedsLocation pins that the preflight's /myself response is
// reused, so resolving the zone costs no extra request on the normal path.
func TestCheckAuthSeedsLocation(t *testing.T) {
	var myselfCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/myself") {
			myselfCalls++
			_ = json.NewEncoder(w).Encode(jiraUserResponse{
				Name: "someone", TimeZone: "Asia/Tashkent",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"key": "ABC"})
	}))
	defer srv.Close()

	f, err := New(Options{BaseURL: srv.URL, PAT: "token", HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	status, err := f.CheckAuth(t.Context(), []string{"ABC"})
	if err != nil {
		t.Fatal(err)
	}
	if status.Name != "someone" {
		t.Errorf("auth status Name = %q, want %q", status.Name, "someone")
	}
	if got := f.location(t.Context()).String(); got != "Asia/Tashkent" {
		t.Errorf("location = %q, want Asia/Tashkent", got)
	}
	if myselfCalls != 1 {
		t.Errorf("/myself called %d times, want 1 (CheckAuth's response should be reused)", myselfCalls)
	}
}
