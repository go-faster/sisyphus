package gitlab

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	chunkgitlab "github.com/go-faster/sisyphus/internal/chunk/gitlab"
	ingestgitlab "github.com/go-faster/sisyphus/internal/ingest/gitlab"
	"github.com/go-faster/sisyphus/internal/notify"
)

// resolvedEvents projects payload and keeps only its thread-resolved events.
func resolvedEvents(t *testing.T, payload ingestgitlab.MRPayload) []notify.Event {
	t.Helper()
	all, err := testProjector().Project(mrEventWithActors(t, payload))
	require.NoError(t, err)

	var out []notify.Event
	for _, e := range all {
		if e.Type == notify.EventMRThreadResolved {
			out = append(out, e)
		}
	}
	return out
}

func TestProjector_ResolvedThread(t *testing.T) {
	events := resolvedEvents(t, ingestgitlab.MRPayload{
		Author: chunkgitlab.User{Username: "alice"},
		Resolutions: []ingestgitlab.Resolution{{
			ThreadID: "d1",
			By:       chunkgitlab.User{Username: "bob", Display: "Bob", URL: "https://example.com/bob"},
			At:       eventTime.Add(-time.Minute),
			Participants: []chunkgitlab.User{
				{Username: "carol"},
				{Username: "bob"},
			},
			Excerpt: "should it be vmauth instead?",
			URL:     "https://example.com/mr/1#note_1",
		}},
	})

	// carol took part; alice owns the MR. bob resolved it and is not told.
	require.Len(t, events, 2)
	var recipients []string
	for _, e := range events {
		recipients = append(recipients, e.Recipient.Key)
	}
	require.Equal(t, []string{"carol", "alice"}, recipients)

	got := events[0]
	require.Equal(t, notify.SourceGitLab, got.Source)
	require.Equal(t, "bob", got.Actor.Key)
	require.Equal(t, "https://example.com/bob", got.Actor.URL)
	require.Equal(t, "MR !1: Fix bug", got.Title)
	require.Equal(t, "should it be vmauth instead?", got.Description)
	require.Equal(t, "https://example.com/mr/1#note_1", got.URL)
	require.Equal(t, []notify.Button{{Text: "Open thread", URL: "https://example.com/mr/1#note_1"}}, got.Buttons)
	require.Equal(t, "group/proj!1", got.ObjectID)
	require.Equal(t, "gitlab_mr_thread_resolved:group/proj!1:d1:carol", got.EventID)
	require.Equal(t, eventTime.Add(-time.Minute), got.OccurredAt)
}

// A thread stays resolved in every payload after it is closed, so without the
// staleness cutoff the first poll after this shipped would announce every
// thread resolved in the fetched window.
func TestProjector_ResolvedThreadStaleness(t *testing.T) {
	events := resolvedEvents(t, ingestgitlab.MRPayload{
		Author: chunkgitlab.User{Username: "alice"},
		Resolutions: []ingestgitlab.Resolution{{
			ThreadID:     "d1",
			By:           chunkgitlab.User{Username: "bob"},
			At:           eventTime.Add(-90 * 24 * time.Hour),
			Participants: []chunkgitlab.User{{Username: "carol"}},
		}},
	})
	require.Empty(t, events)
}

// An unknown resolution time still notifies: over-notifying costs one message
// the dedup key collapses, while under-notifying loses the news silently.
func TestProjector_ResolvedThreadUnknownTimeNotifies(t *testing.T) {
	events := resolvedEvents(t, ingestgitlab.MRPayload{
		Author:      chunkgitlab.User{Username: "alice"},
		Resolutions: []ingestgitlab.Resolution{{ThreadID: "d1", Participants: []chunkgitlab.User{{Username: "carol"}}}},
	})
	require.Len(t, events, 2)
	// No resolver: the renderer says "Someone" rather than naming a colleague
	// who did nothing.
	require.Empty(t, events[0].Actor.Key)
	// No thread URL: the merge request's own page is the fallback.
	require.Equal(t, "https://example.com/mr/1", events[0].URL)
}

// The author resolving their own thread is told nothing, and is not counted
// twice when they also took part in it.
func TestProjector_ResolvedThreadSkipsResolverAndDedupes(t *testing.T) {
	events := resolvedEvents(t, ingestgitlab.MRPayload{
		Author: chunkgitlab.User{Username: "alice"},
		Resolutions: []ingestgitlab.Resolution{{
			ThreadID:     "d1",
			By:           chunkgitlab.User{Username: "alice"},
			At:           eventTime,
			Participants: []chunkgitlab.User{{Username: "alice"}, {Username: "carol"}},
		}},
	})
	require.Len(t, events, 1)
	require.Equal(t, "carol", events[0].Recipient.Key)
}
