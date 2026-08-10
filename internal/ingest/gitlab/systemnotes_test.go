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
			name: "approvals are collected per approver, oldest first",
			discussions: []gitlabDiscussion{
				{Notes: []gitlabNote{
					note(true, approveNote, 2*time.Hour, "carol"),
					note(true, approveNote, time.Hour, "bob"),
				}},
			},
			want: MRActors{
				UpdatedBy: user("carol"),
				Approvals: []chunkgitlab.Approval{
					{User: user("bob"), At: base.Add(time.Hour)},
					{User: user("carol"), At: base.Add(2 * time.Hour)},
				},
			},
		},
		{
			// An approval is standing state: whoever withdrew it is no longer
			// an approver, so nothing about them is news.
			name: "an unapproval withdraws the approval",
			discussions: []gitlabDiscussion{
				{Notes: []gitlabNote{
					note(true, approveNote, time.Hour, "bob"),
					note(true, unapproveNote, 2*time.Hour, "bob"),
				}},
			},
			want: MRActors{UpdatedBy: user("bob")},
		},
		{
			name: "re-approving after an unapproval approves again",
			discussions: []gitlabDiscussion{
				{Notes: []gitlabNote{
					note(true, approveNote, time.Hour, "bob"),
					note(true, unapproveNote, 2*time.Hour, "bob"),
					note(true, approveNote, 3*time.Hour, "bob"),
				}},
			},
			want: MRActors{
				UpdatedBy: user("bob"),
				Approvals: []chunkgitlab.Approval{{User: user("bob"), At: base.Add(3 * time.Hour)}},
			},
		},
		{
			// The username is what a notification matches a recipient on, so
			// an approver GitLab named only by display name cannot be used.
			name: "an approver with no username is dropped",
			discussions: []gitlabDiscussion{
				{Notes: []gitlabNote{{
					System:    true,
					Body:      approveNote,
					CreatedAt: at(time.Hour),
					Author:    &gitlabUser{Name: "Bob"},
				}}},
			},
			want: MRActors{UpdatedBy: chunkgitlab.User{Display: "Bob"}},
		},
		{
			name: "notes with an unparseable time are skipped",
			discussions: []gitlabDiscussion{
				{Notes: []gitlabNote{
					note(true, "assigned to @alice", time.Hour, "bob"),
					{System: true, Body: "assigned to @dave", CreatedAt: "not a time", Author: &gitlabUser{Username: "erin"}},
				}},
			},
			want: MRActors{UpdatedBy: user("bob"), AssignedBy: user("bob"), AssignedAt: base.Add(time.Hour)},
		},
		{
			// The newest note is the assignment, so its timestamp is the one
			// staleness must see. Keeping bob — who assigned alice an hour
			// earlier — would credit him with assigning carol, and date the
			// change to the wrong hour.
			name: "newest note with no author keeps its timestamp",
			discussions: []gitlabDiscussion{
				{Notes: []gitlabNote{
					note(true, "assigned to @alice", time.Hour, "bob"),
					{System: true, Body: "assigned to @carol", CreatedAt: at(2 * time.Hour)},
				}},
			},
			want: MRActors{AssignedAt: base.Add(2 * time.Hour)},
		},
		{
			name: "an older authorless note does not displace the newest",
			discussions: []gitlabDiscussion{
				{Notes: []gitlabNote{
					{System: true, Body: "assigned to @alice", CreatedAt: at(time.Hour)},
					note(true, "assigned to @carol", 2*time.Hour, "bob"),
				}},
			},
			want: MRActors{UpdatedBy: user("bob"), AssignedBy: user("bob"), AssignedAt: base.Add(2 * time.Hour)},
		},
		{
			// An approval note's author is the approver, so one that names
			// nobody records no approval — unlike the membership notes above,
			// there is no separate "who" to degrade.
			name: "approval note with no author records no approval",
			discussions: []gitlabDiscussion{
				{Notes: []gitlabNote{
					{System: true, Body: approveNote, CreatedAt: at(time.Hour)},
				}},
			},
			want: MRActors{AssignedAt: time.Time{}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, mrActors(t.Context(), tt.discussions, "group/project!1"))
		})
	}
}
