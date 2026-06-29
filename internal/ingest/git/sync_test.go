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
)

func TestPrepareLocalDefaultsRepoName(t *testing.T) {
	sources, err := Prepare(context.Background(), []Source{{Root: "/tmp/docs"}}, SyncOptions{})
	require.NoError(t, err)
	require.Len(t, sources, 1)
	require.Equal(t, "docs", sources[0].Repo)
	require.Equal(t, "/tmp/docs", sources[0].Root)
}

func TestDefaultRepoName(t *testing.T) {
	require.Equal(t, "docs", defaultRepoName(Source{URL: "https://gitlab.example.com/group/docs.git"}))
	require.Equal(t, "docs", defaultRepoName(Source{Root: "/tmp/docs"}))
}

func TestPrepareClonesLocalRepository(t *testing.T) {
	remote := newTestRepo(t)
	workDir := t.TempDir()

	sources, err := Prepare(context.Background(), []Source{{
		URL:  remote,
		Repo: "docs",
	}}, SyncOptions{WorkDir: workDir})
	require.NoError(t, err)
	require.Len(t, sources, 1)
	require.Equal(t, filepath.Join(workDir, "docs"), sources[0].Root)

	_, err = os.Stat(filepath.Join(sources[0].Root, "README.md"))
	require.NoError(t, err)
}

func TestSafeDirName(t *testing.T) {
	require.Equal(t, "group_docs", safeDirName("group/docs"))
	require.Equal(t, "repo", safeDirName(""))
}

func TestRedactURL(t *testing.T) {
	require.Equal(t,
		"https://gitlab.example.com/group/docs.git",
		redactURL("https://oauth2:secret@gitlab.example.com/group/docs.git"),
	)
}

func newTestRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Docs\n"), 0o600))

	worktree, err := repo.Worktree()
	require.NoError(t, err)
	_, err = worktree.Add("README.md")
	require.NoError(t, err)
	_, err = worktree.Commit("initial", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "test",
			Email: "test@example.com",
			When:  time.Unix(0, 0),
		},
	})
	require.NoError(t, err)
	return dir
}
