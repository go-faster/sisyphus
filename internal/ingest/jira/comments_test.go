package jira

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	chunkjira "github.com/go-faster/sisyphus/internal/chunk/jira"
)

func TestMentions(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		want []string
	}{
		{"none", "looks good to me", nil},
		// Server/DC names a user by username.
		{"server", "[~jsmith] please look", []string{"jsmith"}},
		// Cloud names them by accountId, which is what identity() reports too.
		{"cloud", "[~accountid:557058:abc-123] ping", []string{"557058:abc-123"}},
		{"several", "[~jsmith] and [~jdoe]", []string{"jsmith", "jdoe"}},
		{"deduped", "[~jsmith] [~jsmith]", []string{"jsmith"}},
		{"not a mention", "see [docs|https://example.com]", nil},
		{"unterminated", "[~jsmith", nil},
		{"empty", "[~]", nil},
		// "@name" is not Jira mention syntax and must not be read as one.
		{"at sign", "@jsmith please look", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, Mentions(tt.body))
		})
	}
}

func FuzzMentions(f *testing.F) {
	for _, s := range []string{
		"[~jsmith] please look", "[~accountid:557058:abc-123] ping",
		"[~jsmith] and [~jdoe]", "[~]", "[~jsmith", "see [docs|https://example.com]",
		"@jsmith", "[~докладчик]",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, body string) {
		seen := make(map[string]struct{})
		for _, id := range Mentions(body) {
			require.NotEmpty(t, id)
			// Every result must be a literal substring of the body, or the
			// projector would address someone the comment never named.
			require.Contains(t, body, id)
			_, dup := seen[id]
			require.False(t, dup, "duplicate mention %q", id)
			seen[id] = struct{}{}
		}
	})
}

func TestLatestComments(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	comments := []chunkjira.Comment{
		{ID: "2", Body: "second", Created: base.Add(time.Minute), AuthorUser: chunkjira.User{ID: "bob"}},
		{ID: "1", Body: "first [~alice]", Created: base, AuthorUser: chunkjira.User{ID: "carol"}},
	}

	got := latestComments(chunkjira.Issue{WebURL: "https://jira.example.com/browse/ABC-1", Comments: comments})
	require.Len(t, got, 2)
	// Sorted oldest first, so the projector can take the newest from the tail.
	require.Equal(t, "1", got[0].ID)
	require.Equal(t, "2", got[1].ID)
	require.Equal(t, []string{"alice"}, got[0].Mentions)
	require.Equal(t, "carol", got[0].Author.ID)
	require.Equal(t, "https://jira.example.com/browse/ABC-1?focusedCommentId=1", got[0].URL)
}

// An event states current state, so a long thread would otherwise ship every
// comment it ever had on every poll.
func TestLatestComments_CapsCount(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	var comments []chunkjira.Comment
	for i := range maxPayloadComments + 5 {
		comments = append(comments, chunkjira.Comment{
			ID:      strconv.Itoa(i),
			Created: base.Add(time.Duration(i) * time.Minute),
		})
	}

	got := latestComments(chunkjira.Issue{WebURL: "https://jira.example.com/browse/ABC-1", Comments: comments})
	require.Len(t, got, maxPayloadComments)
	require.Equal(t, strconv.Itoa(len(comments)-1), got[len(got)-1].ID)
}

// latestComments must not reorder the caller's slice: the same issue is
// chunked from it right after the event is built.
func TestLatestComments_DoesNotReorderInput(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	comments := []chunkjira.Comment{
		{ID: "2", Created: base.Add(time.Minute)},
		{ID: "1", Created: base},
	}

	latestComments(chunkjira.Issue{WebURL: "https://jira.example.com/browse/ABC-1", Comments: comments})
	require.Equal(t, "2", comments[0].ID)
	require.Equal(t, "1", comments[1].ID)
}

func TestCommentURL_SkipsURLWithQuery(t *testing.T) {
	require.Empty(t, commentURL("https://jira.example.com/browse/ABC-1?x=1", "5"))
	require.Empty(t, commentURL("https://jira.example.com/browse/ABC-1", ""))
}
