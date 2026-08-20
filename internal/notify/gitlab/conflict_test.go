package gitlab

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	chunkgitlab "github.com/go-faster/sisyphus/internal/chunk/gitlab"
	ingestgitlab "github.com/go-faster/sisyphus/internal/ingest/gitlab"
	"github.com/go-faster/sisyphus/internal/notify"
)

func conflictEvent(t *testing.T, ref ingestgitlab.ConflictRef, at time.Time) []notify.Event {
	t.Helper()
	e, err := ingestgitlab.EventFromConflict(ref, at)
	require.NoError(t, err)
	// A stale-by-any-measure sweep clock, to prove Staleness does not gate a
	// conflict: it has no timestamp of its own to gate on.
	out, err := Projector{Staleness: notify.Staleness{MaxAge: time.Hour}}.Project(e)
	require.NoError(t, err)
	return out
}

func TestProjectConflict(t *testing.T) {
	at := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	ref := ingestgitlab.ConflictRef{
		Project: "g/p",
		MR: ingestgitlab.ConflictMR{
			IID:          7,
			Title:        "Fix the thing",
			WebURL:       "https://gitlab.example.com/g/p/-/merge_requests/7",
			SourceBranch: "feature",
			TargetBranch: "main",
			SHA:          "deadbeef",
			Author:       chunkgitlab.User{Username: "alice"},
			Assignees:    []string{"bob", "alice"},
			Updated:      at.Add(-90 * 24 * time.Hour),
		},
	}

	t.Run("author then assignees, deduped", func(t *testing.T) {
		out := conflictEvent(t, ref, at)
		require.Len(t, out, 2)
		require.Equal(t, "alice", out[0].Recipient.Key)
		require.Equal(t, "bob", out[1].Recipient.Key)
		for _, e := range out {
			require.Equal(t, notify.EventMRConflict, e.Type)
			require.Equal(t, "g/p!7", e.ObjectID)
			require.Equal(t, ref.MR.WebURL, e.URL)
			require.Equal(t, "feature can no longer be merged into main: rebase or resolve the conflicts.", e.Description)
			require.Equal(t, notify.Actor{}, e.Actor, "a conflict names no actor")
			require.Equal(t, []notify.Button{{Text: "Open merge request", URL: ref.MR.WebURL}}, e.Buttons)
		}
	})

	t.Run("dedup id is keyed on the head sha", func(t *testing.T) {
		first := conflictEvent(t, ref, at)
		// The same conflict, re-observed a tick later: same id, so the outbox
		// suppresses it.
		again := conflictEvent(t, ref, at.Add(15*time.Minute))
		require.Equal(t, first[0].EventID, again[0].EventID)

		pushed := ref
		pushed.MR.SHA = "cafebabe"
		after := conflictEvent(t, pushed, at.Add(time.Hour))
		require.NotEqual(t, first[0].EventID, after[0].EventID, "a new head that still conflicts is news again")
	})

	t.Run("reviewers are not told", func(t *testing.T) {
		noMembers := ref
		noMembers.MR.Assignees = nil
		out := conflictEvent(t, noMembers, at)
		require.Len(t, out, 1)
		require.Equal(t, "alice", out[0].Recipient.Key)
	})

	t.Run("unnamed author notifies nobody", func(t *testing.T) {
		anon := ref
		anon.MR.Author = chunkgitlab.User{}
		anon.MR.Assignees = nil
		require.Empty(t, conflictEvent(t, anon, at))
	})

	t.Run("target branch only", func(t *testing.T) {
		noSource := ref
		noSource.MR.SourceBranch = ""
		out := conflictEvent(t, noSource, at)
		require.Equal(t, "Cannot be merged into main: rebase or resolve the conflicts.", out[0].Description)
	})
}
