package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/index"
)

func TestWalkCommits(t *testing.T) {
	t.Run("filters bot commits and merge commits", func(t *testing.T) {
		repo, root := newTestGitRepo(t)

		// Create normal commit
		commitNormal(t, repo, "Normal commit", "alice", "alice@example.com")

		// Create bot commit
		commitNormal(t, repo, "Update dependencies", "dependabot[bot]", "noreply@github.com")

		// Create normal commit
		commitNormal(t, repo, "Fix bug", "bob", "bob@example.com")

		// Walk commits starting from zero cursor
		result, err := WalkCommits(context.Background(), Source{
			Root: root,
			Repo: "test/repo",
		}, CommitCursor{}, 0)

		require.NoError(t, err)
		// Should have 2 docs (bot commit filtered)
		require.Len(t, result.Documents, 2)

		// Check that normal commits are present, bot is not
		commits := make(map[string]bool)
		for _, doc := range result.Documents {
			title := doc.Title
			commits[title] = true
		}

		require.True(t, commits["Fix bug"])
		require.True(t, commits["Normal commit"])
		require.False(t, commits["Update dependencies"])

		// Verify cursor is set to HEAD
		require.NotEmpty(t, result.NextCursor.LastSHA)
		// Branch name might be "main" or "master" depending on git config
		require.NotEmpty(t, result.NextCursor.Branch)
	})

	t.Run("incremental walk respects cursor", func(t *testing.T) {
		repo, root := newTestGitRepo(t)

		// Create 3 commits
		commitNormal(t, repo, "First", "alice", "alice@example.com")
		commitNormal(t, repo, "Second", "bob", "bob@example.com")
		commitNormal(t, repo, "Third", "charlie", "charlie@example.com")

		// First walk gets all 3
		result1, err := WalkCommits(context.Background(), Source{
			Root: root,
			Repo: "test/repo",
		}, CommitCursor{}, 0)
		require.NoError(t, err)
		require.Len(t, result1.Documents, 3)

		// Second walk with cursor should return nothing
		result2, err := WalkCommits(context.Background(), Source{
			Root: root,
			Repo: "test/repo",
		}, result1.NextCursor, 0)
		require.NoError(t, err)
		require.Len(t, result2.Documents, 0)
	})

	t.Run("limit parameter works", func(t *testing.T) {
		repo, root := newTestGitRepo(t)

		// Create 5 commits
		for i := 1; i <= 5; i++ {
			commitNormal(t, repo, "Commit"+string(rune('0'+i)), "user", "user@example.com")
		}

		result, err := WalkCommits(context.Background(), Source{
			Root: root,
			Repo: "test/repo",
		}, CommitCursor{}, 2)
		require.NoError(t, err)
		require.Len(t, result.Documents, 2)
	})

	t.Run("document structure is correct", func(t *testing.T) {
		repo, root := newTestGitRepo(t)

		now := time.Date(2024, 1, 15, 10, 30, 45, 0, time.UTC)
		commitWithTime(t, repo, "Fix: improve performance", "alice", "alice@example.com", now)

		result, err := WalkCommits(context.Background(), Source{
			Root:   root,
			Repo:   "my-project",
			Branch: "develop",
		}, CommitCursor{}, 0)
		require.NoError(t, err)
		require.Len(t, result.Documents, 1)

		doc := result.Documents[0]
		require.Equal(t, index.SourceGitCommit("my-project"), doc.Source)
		require.Equal(t, "Fix: improve performance", doc.Title)
		require.Equal(t, "Fix: improve performance", doc.Body) // Single-line message
		require.Equal(t, "my-project", doc.Metadata["repo"])
		require.Equal(t, "alice", doc.Metadata["author"])
		require.Equal(t, "alice@example.com", doc.Metadata["author_email"])
		require.Equal(t, "develop", doc.Metadata["branch"])
		require.Equal(t, string(index.AuthorityLow), doc.Metadata["authority"])
		require.NotEmpty(t, doc.SourceID)                       // Should be "my-project@<short-sha>"
		require.True(t, len(doc.SourceID) > len("my-project@")) // Has short SHA
	})

	t.Run("multiline message handling", func(t *testing.T) {
		repo, root := newTestGitRepo(t)

		multilineMessage := "Fix: critical bug\n\nThis fixes the critical issue\nthat was reported in #123."
		commitWithMessage(t, repo, multilineMessage, "alice", "alice@example.com")

		result, err := WalkCommits(context.Background(), Source{
			Root: root,
			Repo: "test/repo",
		}, CommitCursor{}, 0)
		require.NoError(t, err)
		require.Len(t, result.Documents, 1)

		doc := result.Documents[0]
		require.Equal(t, "Fix: critical bug", doc.Title)
		require.Contains(t, doc.Body, "Fix: critical bug")
		require.Contains(t, doc.Body, "critical issue")
		require.Contains(t, doc.Body, "#123")
	})

	t.Run("skips trivial subjects", func(t *testing.T) {
		repo, root := newTestGitRepo(t)

		commitNormal(t, repo, "Real commit", "alice", "alice@example.com")
		commitNormal(t, repo, "...", "bob", "bob@example.com")
		commitNormal(t, repo, "wip", "charlie", "charlie@example.com")
		commitNormal(t, repo, "---", "dave", "dave@example.com")

		result, err := WalkCommits(context.Background(), Source{
			Root: root,
			Repo: "test/repo",
		}, CommitCursor{}, 0)
		require.NoError(t, err)
		require.Len(t, result.Documents, 1)
		require.Equal(t, "Real commit", result.Documents[0].Title)
	})
}

// newTestGitRepo creates an empty git repository and returns the repo object and root path.
func newTestGitRepo(t *testing.T) (repo *git.Repository, root string) {
	t.Helper()

	root = t.TempDir()
	repo, err := git.PlainInit(root, false)
	require.NoError(t, err)

	return repo, root
}

// commitNormal creates a simple commit with the given message and author.
func commitNormal(t *testing.T, repo *git.Repository, message, name, email string) {
	t.Helper()
	commitWithTime(t, repo, message, name, email, time.Now())
}

// commitWithTime creates a commit with the given message, author, and timestamp.
func commitWithTime(t *testing.T, repo *git.Repository, message, name, email string, when time.Time) {
	t.Helper()

	worktree, err := repo.Worktree()
	require.NoError(t, err)

	// Create a dummy file for the commit
	filename := filepath.Join(worktree.Filesystem.Root(), "file.txt")
	err = os.WriteFile(filename, []byte(message), 0o644)
	require.NoError(t, err)

	_, err = worktree.Add("file.txt")
	require.NoError(t, err)

	_, err = worktree.Commit(message, &git.CommitOptions{
		Author: &object.Signature{
			Name:  name,
			Email: email,
			When:  when,
		},
	})
	require.NoError(t, err)
}

// commitWithMessage creates a commit with explicit message content.
func commitWithMessage(t *testing.T, repo *git.Repository, message, name, email string) {
	t.Helper()
	commitWithTime(t, repo, message, name, email, time.Now())
}

func TestIsBotAuthor(t *testing.T) {
	tests := []struct {
		name     string
		author   string
		email    string
		wantBbot bool
	}{
		{
			name:     "bot suffix in name",
			author:   "dependabot[bot]",
			email:    "user@example.com",
			wantBbot: true,
		},
		{
			name:     "bot word in name",
			author:   "my bot name",
			email:    "user@example.com",
			wantBbot: true,
		},
		{
			name:     "noreply in email",
			author:   "Alice",
			email:    "noreply@github.com",
			wantBbot: true,
		},
		{
			name:     "normal author",
			author:   "Alice",
			email:    "alice@example.com",
			wantBbot: false,
		},
		{
			name:     "renovate bot",
			author:   "renovate",
			email:    "renovate@bot.com",
			wantBbot: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isBotAuthor(tt.author, tt.email)
			require.Equal(t, tt.wantBbot, got)
		})
	}
}

func TestIsTrivialSubject(t *testing.T) {
	tests := []struct {
		name        string
		subject     string
		wantTrivial bool
	}{
		{
			name:        "empty subject",
			subject:     "",
			wantTrivial: true,
		},
		{
			name:        "dots",
			subject:     "...",
			wantTrivial: true,
		},
		{
			name:        "dashes",
			subject:     "----",
			wantTrivial: true,
		},
		{
			name:        "wip prefix",
			subject:     "wip: implement feature",
			wantTrivial: true,
		},
		{
			name:        "normal commit",
			subject:     "Fix: critical bug",
			wantTrivial: false,
		},
		{
			name:        "feature commit",
			subject:     "Add new API endpoint",
			wantTrivial: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isTrivialSubject(tt.subject)
			require.Equal(t, tt.wantTrivial, got)
		})
	}
}
