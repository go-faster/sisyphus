package gitlab

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	chunkgitlab "github.com/go-faster/sisyphus/internal/chunk/gitlab"
	"github.com/go-faster/sisyphus/internal/event"
)

func TestEventFromMergeRequest(t *testing.T) {
	updated := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	ref := MergeRequestRef{
		Project: "group/project",
		MR: chunkgitlab.MergeRequest{
			IID:               42,
			Title:             "Fix flaky test",
			Author:            chunkgitlab.User{Username: "carol"},
			WebURL:            "https://gitlab.example.com/group/project/-/merge_requests/42",
			Assignees:         []string{"alice"},
			Reviewers:         []string{"bob"},
			UpdatedBy:         chunkgitlab.User{Username: "erin", Display: "Erin", URL: "https://gitlab.example.com/erin"},
			AssignedBy:        chunkgitlab.User{Username: "dave", Display: "Dave", URL: "https://gitlab.example.com/dave"},
			ReviewRequestedBy: chunkgitlab.User{Username: "frank", Display: "Frank", URL: "https://gitlab.example.com/frank"},
			Updated:           updated,
		},
	}

	e, err := EventFromMergeRequest(ref)
	require.NoError(t, err)

	require.Equal(t, event.SourceGitLab, e.Source)
	require.Equal(t, event.TypeMRUpdated, e.Type)
	require.Equal(t, "gitlab_mr_update:group/project!42:2026-03-04T05:06:07Z", e.ID)
	require.Equal(t, "group/project!42", e.Subject.ID)
	require.Equal(t, ref.MR.WebURL, e.Subject.URL)
	require.Equal(t, "MR !42: Fix flaky test", e.Subject.Title)
	// The actor is the last system-note author, never the MR's opener.
	require.Equal(t, "erin", e.Actor.Key)
	require.Equal(t, "Erin", e.Actor.Display)
	require.Equal(t, "https://gitlab.example.com/erin", e.Actor.URL)
	require.NotEqual(t, ref.MR.Author.Username, e.Actor.Key)
	require.Equal(t, updated, e.OccurredAt)
	require.Equal(t, "group/project", e.Attributes["project"])

	var p MRPayload
	require.NoError(t, e.DecodePayload(MRPayloadVersion, &p))
	require.Equal(t, []string{"alice"}, p.Assignees)
	require.Equal(t, []string{"bob"}, p.Reviewers)
	require.Equal(t, ref.MR.AssignedBy, p.AssignedBy)
	require.Equal(t, ref.MR.ReviewRequestedBy, p.ReviewRequestedBy)
	require.Equal(t, ref.MR.Author, p.Author)
}

// A merged MR carries the merge in its payload: the state, when it happened
// and who did it, all three of which the notification gateway needs to tell a
// merge that just landed from one that landed months ago.
func TestEventFromMergeRequestCarriesMerge(t *testing.T) {
	mergedAt := time.Date(2026, 3, 4, 5, 0, 0, 0, time.UTC)
	e, err := EventFromMergeRequest(MergeRequestRef{
		Project: "group/project",
		MR: chunkgitlab.MergeRequest{
			IID:      42,
			Author:   chunkgitlab.User{Username: "carol"},
			State:    "merged",
			MergedAt: mergedAt,
			MergedBy: chunkgitlab.User{Username: "dave", Display: "Dave", URL: "https://gitlab.example.com/dave"},
			Updated:  mergedAt,
		},
	})
	require.NoError(t, err)

	var p MRPayload
	require.NoError(t, e.DecodePayload(MRPayloadVersion, &p))
	require.Equal(t, "merged", p.State)
	require.Equal(t, mergedAt, p.MergedAt)
	require.Equal(t, "dave", p.MergedBy.Username)
	require.Equal(t, "https://gitlab.example.com/dave", p.MergedBy.URL)
	require.Equal(t, "carol", p.Author.Username)
}

// An MR whose system notes named nobody carries no actor at all, rather than
// falling back to its author.
func TestEventFromMergeRequestWithoutSystemNotesHasNoActor(t *testing.T) {
	e, err := EventFromMergeRequest(MergeRequestRef{
		Project: "group/project",
		MR: chunkgitlab.MergeRequest{
			IID:       7,
			Author:    chunkgitlab.User{Username: "carol", URL: "https://gitlab.example.com/carol"},
			Assignees: []string{"alice"},
			Updated:   time.Unix(0, 0).UTC(),
		},
	})
	require.NoError(t, err)
	require.True(t, e.Actor.Zero())

	var p MRPayload
	require.NoError(t, e.DecodePayload(MRPayloadVersion, &p))
	require.True(t, p.AssignedBy.Zero())
	require.True(t, p.ReviewRequestedBy.Zero())
}

// The ID must be stable per (MR, updated_at): re-fetching an unchanged MR —
// which every cursor-bounded poll does — has to look like the same occurrence
// to handlers that dedup on it, and an edit has to look like a new one.
func TestEventFromMergeRequestIDStability(t *testing.T) {
	base := MergeRequestRef{
		Project: "group/project",
		MR: chunkgitlab.MergeRequest{
			IID:     7,
			Updated: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
	}

	first, err := EventFromMergeRequest(base)
	require.NoError(t, err)

	same := base
	same.MR.Title = "retitled but not touched upstream"
	sameEvent, err := EventFromMergeRequest(same)
	require.NoError(t, err)
	require.Equal(t, first.ID, sameEvent.ID)

	edited := base
	edited.MR.Updated = base.MR.Updated.Add(time.Minute)
	editedEvent, err := EventFromMergeRequest(edited)
	require.NoError(t, err)
	require.NotEqual(t, first.ID, editedEvent.ID)
}

func TestEventFromConflict(t *testing.T) {
	at := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	ref := ConflictRef{
		Project: "g/p",
		MR: ConflictMR{
			IID:          7,
			Title:        "Fix the thing",
			WebURL:       "https://gitlab.example.com/g/p/-/merge_requests/7",
			SourceBranch: "feature",
			TargetBranch: "main",
			SHA:          "deadbeef",
			Author:       chunkgitlab.User{Username: "alice"},
			Assignees:    []string{"bob"},
			Status:       "conflict",
		},
	}

	e, err := EventFromConflict(ref, at)
	require.NoError(t, err)
	require.Equal(t, event.TypeMRConflict, e.Type)
	require.Equal(t, event.SourceGitLab, e.Source)
	require.Equal(t, "gitlab_mr_conflict:g/p!7:deadbeef", e.ID)
	require.Equal(t, "g/p!7", e.Subject.ID)
	require.Equal(t, "MR !7: Fix the thing", e.Subject.Title)
	require.Equal(t, at, e.OccurredAt)
	// Nobody performed the conflict, so naming an actor would name the wrong
	// person.
	require.True(t, e.Actor.Zero())

	var p ConflictPayload
	require.NoError(t, e.DecodePayload(ConflictPayloadVersion, &p))
	require.Equal(t, "alice", p.Author.Username)
	require.Equal(t, []string{"bob"}, p.Assignees)
	require.Equal(t, "deadbeef", p.SHA)
	require.Equal(t, "main", p.TargetBranch)
}
