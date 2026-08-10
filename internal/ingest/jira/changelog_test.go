package jira

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	chunkjira "github.com/go-faster/sisyphus/internal/chunk/jira"
)

func TestChangelogActors(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) string { return base.Add(d).Format("2006-01-02T15:04:05.000-0700") }
	user := func(account, display string) *jiraUser {
		return &jiraUser{AccountID: account, DisplayName: display}
	}
	assigneeItem := jiraHistoryItem{Field: "assignee"}
	labelItem := jiraHistoryItem{Field: "labels"}

	tests := []struct {
		name           string
		changelog      *jiraChangelog
		wantUpdatedBy  chunkjira.User
		wantAssignedBy chunkjira.User
		wantAssignedAt time.Duration // offset from base; zero means no assignment
	}{
		{
			name:      "nil changelog",
			changelog: nil,
		},
		{
			name:      "no histories",
			changelog: &jiraChangelog{},
		},
		{
			name: "assignee change is the assigner, latest change is the updater",
			changelog: &jiraChangelog{Histories: []jiraHistory{
				{Author: user("acc-bob", "Bob"), Created: at(time.Hour), Items: []jiraHistoryItem{assigneeItem}},
				{Author: user("acc-dave", "Dave"), Created: at(2 * time.Hour), Items: []jiraHistoryItem{labelItem}},
			}},
			wantUpdatedBy:  chunkjira.User{ID: "acc-dave", Display: "Dave", URL: "https://jira.example.com/jira/people/acc-dave"},
			wantAssignedBy: chunkjira.User{ID: "acc-bob", Display: "Bob", URL: "https://jira.example.com/jira/people/acc-bob"},
			wantAssignedAt: time.Hour,
		},
		{
			name: "newest assignee change wins over an older one",
			changelog: &jiraChangelog{Histories: []jiraHistory{
				{Author: user("acc-bob", "Bob"), Created: at(time.Hour), Items: []jiraHistoryItem{assigneeItem}},
				{Author: user("acc-erin", "Erin"), Created: at(3 * time.Hour), Items: []jiraHistoryItem{assigneeItem}},
			}},
			wantUpdatedBy:  chunkjira.User{ID: "acc-erin", Display: "Erin", URL: "https://jira.example.com/jira/people/acc-erin"},
			wantAssignedBy: chunkjira.User{ID: "acc-erin", Display: "Erin", URL: "https://jira.example.com/jira/people/acc-erin"},
			wantAssignedAt: 3 * time.Hour,
		},
		{
			// Jira documents no ordering guarantee, so the newest entry is
			// found by timestamp rather than by position.
			name: "newest-first ordering still picks the newest",
			changelog: &jiraChangelog{Histories: []jiraHistory{
				{Author: user("acc-erin", "Erin"), Created: at(3 * time.Hour), Items: []jiraHistoryItem{assigneeItem}},
				{Author: user("acc-bob", "Bob"), Created: at(time.Hour), Items: []jiraHistoryItem{assigneeItem}},
			}},
			wantUpdatedBy:  chunkjira.User{ID: "acc-erin", Display: "Erin", URL: "https://jira.example.com/jira/people/acc-erin"},
			wantAssignedBy: chunkjira.User{ID: "acc-erin", Display: "Erin", URL: "https://jira.example.com/jira/people/acc-erin"},
			wantAssignedAt: 3 * time.Hour,
		},
		{
			name: "fieldId matches when field is localized",
			changelog: &jiraChangelog{Histories: []jiraHistory{
				{Author: user("acc-bob", "Bob"), Created: at(time.Hour), Items: []jiraHistoryItem{{Field: "Исполнитель", FieldID: "assignee"}}},
			}},
			wantUpdatedBy:  chunkjira.User{ID: "acc-bob", Display: "Bob", URL: "https://jira.example.com/jira/people/acc-bob"},
			wantAssignedBy: chunkjira.User{ID: "acc-bob", Display: "Bob", URL: "https://jira.example.com/jira/people/acc-bob"},
			wantAssignedAt: time.Hour,
		},
		{
			// Server/DC has no accountId; identity falls back to name, and
			// the profile URL takes the other shape.
			name: "server user",
			changelog: &jiraChangelog{Histories: []jiraHistory{
				{Author: &jiraUser{Name: "bob", DisplayName: "Bob"}, Created: at(time.Hour), Items: []jiraHistoryItem{assigneeItem}},
			}},
			wantUpdatedBy:  chunkjira.User{ID: "bob", Display: "Bob", URL: "https://jira.example.com/secure/ViewProfile.jspa?name=bob"},
			wantAssignedBy: chunkjira.User{ID: "bob", Display: "Bob", URL: "https://jira.example.com/secure/ViewProfile.jspa?name=bob"},
			wantAssignedAt: time.Hour,
		},
		{
			name: "entries with an unparseable time are skipped",
			changelog: &jiraChangelog{Histories: []jiraHistory{
				{Author: user("acc-bob", "Bob"), Created: at(time.Hour), Items: []jiraHistoryItem{assigneeItem}},
				{Author: user("acc-erin", "Erin"), Created: "not a time", Items: []jiraHistoryItem{assigneeItem}},
			}},
			wantUpdatedBy:  chunkjira.User{ID: "acc-bob", Display: "Bob", URL: "https://jira.example.com/jira/people/acc-bob"},
			wantAssignedBy: chunkjira.User{ID: "acc-bob", Display: "Bob", URL: "https://jira.example.com/jira/people/acc-bob"},
			wantAssignedAt: time.Hour,
		},
		{
			// The when survives an author Jira cannot name, so staleness can
			// still prove this assignment old; only the who degrades.
			name: "newest entry with no author keeps its timestamp",
			changelog: &jiraChangelog{Histories: []jiraHistory{
				{Author: user("acc-bob", "Bob"), Created: at(time.Hour), Items: []jiraHistoryItem{assigneeItem}},
				{Author: nil, Created: at(2 * time.Hour), Items: []jiraHistoryItem{assigneeItem}},
			}},
			wantAssignedAt: 2 * time.Hour,
		},
		{
			name: "an older authorless entry does not displace the newest",
			changelog: &jiraChangelog{Histories: []jiraHistory{
				{Author: nil, Created: at(time.Hour), Items: []jiraHistoryItem{assigneeItem}},
				{Author: user("acc-erin", "Erin"), Created: at(2 * time.Hour), Items: []jiraHistoryItem{assigneeItem}},
			}},
			wantUpdatedBy:  chunkjira.User{ID: "acc-erin", Display: "Erin", URL: "https://jira.example.com/jira/people/acc-erin"},
			wantAssignedBy: chunkjira.User{ID: "acc-erin", Display: "Erin", URL: "https://jira.example.com/jira/people/acc-erin"},
			wantAssignedAt: 2 * time.Hour,
		},
		{
			name: "no assignee change leaves the assigner unset",
			changelog: &jiraChangelog{Histories: []jiraHistory{
				{Author: user("acc-dave", "Dave"), Created: at(time.Hour), Items: []jiraHistoryItem{labelItem}},
			}},
			wantUpdatedBy: chunkjira.User{ID: "acc-dave", Display: "Dave", URL: "https://jira.example.com/jira/people/acc-dave"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actors := changelogActors(t.Context(), tt.changelog, "https://jira.example.com", "ABC-1")
			require.Equal(t, tt.wantUpdatedBy, actors.UpdatedBy)
			require.Equal(t, tt.wantAssignedBy, actors.AssignedBy)
			if tt.wantAssignedAt == 0 {
				require.True(t, actors.AssignedAt.IsZero())
				return
			}
			require.Equal(t, base.Add(tt.wantAssignedAt), actors.AssignedAt.UTC())
		})
	}
}

func TestAssignedAtCreation(t *testing.T) {
	t.Parallel()

	entry := func(field string) jiraHistory {
		return jiraHistory{
			Author:  &jiraUser{AccountID: "acc-bob"},
			Created: "2026-06-01T12:00:00.000+0000",
			Items:   []jiraHistoryItem{{Field: field}},
		}
	}

	tests := []struct {
		name      string
		changelog *jiraChangelog
		want      bool
	}{
		{
			// No expand=changelog, or Jira declined to send one: the issue's
			// history is simply not in hand, which says nothing either way.
			name:      "nil changelog proves nothing",
			changelog: nil,
		},
		{
			name:      "an issue never edited was assigned when it was filed",
			changelog: &jiraChangelog{},
			want:      true,
		},
		{
			name:      "edits that never touched the assignee",
			changelog: &jiraChangelog{Total: 2, Histories: []jiraHistory{entry("description"), entry("Sprint")}},
			want:      true,
		},
		{
			name:      "an assignee entry means it was assigned later",
			changelog: &jiraChangelog{Total: 2, Histories: []jiraHistory{entry("description"), entry("assignee")}},
		},
		{
			name:      "a localized assignee entry still counts",
			changelog: &jiraChangelog{Total: 1, Histories: []jiraHistory{{Created: "2026-06-01T12:00:00.000+0000", Items: []jiraHistoryItem{{Field: "Исполнитель", FieldID: "assignee"}}}}},
		},
		{
			// The missing entry could be in the part Jira did not send, so
			// absence is not evidence and AssignedAt stays unknown.
			name:      "a truncated changelog proves nothing",
			changelog: &jiraChangelog{Total: 20, MaxResults: 2, Histories: []jiraHistory{entry("description"), entry("Sprint")}},
		},
		{
			name:      "a later page proves nothing",
			changelog: &jiraChangelog{StartAt: 2, Total: 4, Histories: []jiraHistory{entry("description"), entry("Sprint")}},
		},
		{
			// Deployments that omit the paging fields report Total 0; the
			// entries they did send are taken as the whole history.
			name:      "paging fields absent",
			changelog: &jiraChangelog{Histories: []jiraHistory{entry("description")}},
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, assignedAtCreation(tt.changelog))
		})
	}
}
