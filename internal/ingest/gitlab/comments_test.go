package gitlab

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	chunkgitlab "github.com/go-faster/sisyphus/internal/chunk/gitlab"
)

func TestMentions(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		want []string
	}{
		{"none", "looks good to me", nil},
		{"one", "@alice please review", []string{"alice"}},
		{"mid sentence", "cc @alice and @bob", []string{"alice", "bob"}},
		{"parenthesized", "(@alice)", []string{"alice"}},
		{"trailing punctuation", "thanks @alice.", []string{"alice"}},
		{"dotted username", "ping @alice.smith please", []string{"alice.smith"}},
		{"dashes and underscores", "@a-b_c1", []string{"a-b_c1"}},
		{"deduped", "@alice @alice", []string{"alice"}},
		// An email is the common false positive: the "@" has a word character
		// before it, so it is not a mention.
		{"email", "mail me at alice@example.com", nil},
		{"path", "see docs/@latest", nil},
		{"double at", "@@alice", nil},
		// A group is not a person this gateway can address; the first segment
		// matches but resolves to no configured identity.
		{"group", "@group/subgroup ping", []string{"group"}},
		{"newline separated", "hi @alice\n@bob too", []string{"alice", "bob"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, Mentions(tt.body))
		})
	}
}

func FuzzMentions(f *testing.F) {
	for _, s := range []string{
		"@alice please review", "mail me at alice@example.com", "@group/subgroup",
		"cc @alice and @bob", "@@alice", "@a.b-c_d.", "докладываю @алиса",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, body string) {
		seen := make(map[string]struct{})
		for _, name := range Mentions(body) {
			require.NotEmpty(t, name)
			// Every result must be a literal substring of the body, or the
			// projector would address someone the comment never named.
			require.Contains(t, body, "@"+name)
			_, dup := seen[name]
			require.False(t, dup, "duplicate mention %q", name)
			seen[name] = struct{}{}
		}
	})
}

func TestLatestComments(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	threads := []chunkgitlab.Thread{
		{Comments: []chunkgitlab.Comment{
			{ID: "2", Body: "second", Created: base.Add(time.Minute), AuthorUser: chunkgitlab.User{Username: "bob"}},
		}},
		{Comments: []chunkgitlab.Comment{
			{ID: "1", Body: "first @alice", Created: base, AuthorUser: chunkgitlab.User{Username: "carol"}},
		}},
	}

	got := latestComments(threads, "https://example.com/mr/1")
	require.Len(t, got, 2)
	// Flattened across threads and sorted oldest first, so the projector can
	// take the newest from the tail.
	require.Equal(t, "1", got[0].ID)
	require.Equal(t, "2", got[1].ID)
	require.Equal(t, []string{"alice"}, got[0].Mentions)
	require.Equal(t, "carol", got[0].Author.Username)
	require.Equal(t, "https://example.com/mr/1#note_1", got[0].URL)
}

// An event states current state, so a long thread would otherwise ship every
// comment it ever had on every poll.
func TestLatestComments_CapsCount(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	var comments []chunkgitlab.Comment
	for i := range maxPayloadComments + 5 {
		comments = append(comments, chunkgitlab.Comment{
			ID:      strings.Repeat("x", i+1),
			Created: base.Add(time.Duration(i) * time.Minute),
		})
	}

	got := latestComments([]chunkgitlab.Thread{{Comments: comments}}, "https://example.com/mr/1")
	require.Len(t, got, maxPayloadComments)
	// The newest are the ones kept.
	require.Equal(t, strings.Repeat("x", len(comments)), got[len(got)-1].ID)
}

func TestLatestComments_NoObjectURL(t *testing.T) {
	got := latestComments([]chunkgitlab.Thread{{Comments: []chunkgitlab.Comment{{ID: "1"}}}}, "")
	require.Len(t, got, 1)
	require.Empty(t, got[0].URL)
}
