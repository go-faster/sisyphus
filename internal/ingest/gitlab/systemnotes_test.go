package gitlab

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	chunkgitlab "github.com/go-faster/sisyphus/internal/chunk/gitlab"
)

func TestMRActors(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) string { return base.Add(d).Format(time.RFC3339) }
	note := func(system bool, body string, d time.Duration, username string) gitlabNote {
		return gitlabNote{
			System:    system,
			Body:      body,
			CreatedAt: at(d),
			Author: &gitlabUser{
				Username: username,
				Name:     username + " name",
				WebURL:   "https://gitlab.example.com/" + username,
			},
		}
	}
	user := func(username string) chunkgitlab.User {
		return chunkgitlab.User{
			Username: username,
			Display:  username + " name",
			URL:      "https://gitlab.example.com/" + username,
		}
	}

	tests := []struct {
		name        string
		discussions []gitlabDiscussion
		want        MRActors
	}{
		{
			name: "no discussions",
		},
		{
			name: "human comments name nobody",
			discussions: []gitlabDiscussion{
				{Notes: []gitlabNote{note(false, "assigned to @alice", time.Hour, "mallory")}},
			},
		},
		{
			name: "assignment, review request and a later unrelated change",
			discussions: []gitlabDiscussion{
				{Notes: []gitlabNote{
					note(true, "assigned to @alice", time.Hour, "bob"),
					note(true, "requested review from @carol", 2*time.Hour, "dave"),
					note(true, "added 1 commit", 3*time.Hour, "erin"),
				}},
			},
			want: MRActors{
				UpdatedBy:         user("erin"),
				AssignedBy:        user("bob"),
				ReviewRequestedBy: user("dave"),
				AssignedAt:        base.Add(time.Hour),
				ReviewRequestedAt: base.Add(2 * time.Hour),
			},
		},
		{
			// Notes are not guaranteed to arrive in order, so the newest is
			// found by timestamp rather than by position.
			name: "newest wins regardless of order",
			discussions: []gitlabDiscussion{
				{Notes: []gitlabNote{note(true, "assigned to @carol", 5*time.Hour, "erin")}},
				{Notes: []gitlabNote{note(true, "assigned to @alice", time.Hour, "bob")}},
			},
			want: MRActors{UpdatedBy: user("erin"), AssignedBy: user("erin"), AssignedAt: base.Add(5 * time.Hour)},
		},
		{
			name: "reassignment and unassignment count as assignment",
			discussions: []gitlabDiscussion{
				{Notes: []gitlabNote{
					note(true, "unassigned @alice", time.Hour, "bob"),
					note(true, "unassigned @bob and assigned @carol", 2*time.Hour, "dave"),
				}},
			},
			want: MRActors{UpdatedBy: user("dave"), AssignedBy: user("dave"), AssignedAt: base.Add(2 * time.Hour)},
		},
		{
			name: "removing a review request is a review-request change",
			discussions: []gitlabDiscussion{
				{Notes: []gitlabNote{note(true, "removed review request for @carol", time.Hour, "dave")}},
			},
			want: MRActors{UpdatedBy: user("dave"), ReviewRequestedBy: user("dave"), ReviewRequestedAt: base.Add(time.Hour)},
		},
		{
			name: "notes with no author or unparseable time are skipped",
			discussions: []gitlabDiscussion{
				{Notes: []gitlabNote{
					note(true, "assigned to @alice", time.Hour, "bob"),
					{System: true, Body: "assigned to @carol", CreatedAt: at(2 * time.Hour)},
					{System: true, Body: "assigned to @dave", CreatedAt: "not a time", Author: &gitlabUser{Username: "erin"}},
				}},
			},
			want: MRActors{UpdatedBy: user("bob"), AssignedBy: user("bob"), AssignedAt: base.Add(time.Hour)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, mrActors(tt.discussions))
		})
	}
}
