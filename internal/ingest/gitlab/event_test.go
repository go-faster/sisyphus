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
			IID:       42,
			Title:     "Fix flaky test",
			Author:    "carol",
			WebURL:    "https://gitlab.example.com/group/project/-/merge_requests/42",
			Assignees: []string{"alice"},
			Reviewers: []string{"bob"},
			Updated:   updated,
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
	require.Equal(t, "carol", e.Actor.Key)
	require.Equal(t, updated, e.OccurredAt)
	require.Equal(t, "group/project", e.Attributes["project"])

	var p MRPayload
	require.NoError(t, e.DecodePayload(&p))
	require.Equal(t, []string{"alice"}, p.Assignees)
	require.Equal(t, []string{"bob"}, p.Reviewers)
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
