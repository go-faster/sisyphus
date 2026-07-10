package content

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/go-faster/sisyphus/internal/index"
)

func TestLocalRepoReader(t *testing.T) {
	tmpDir := t.TempDir()
	repoPath := filepath.Join(tmpDir, "myrepo")
	require.NoError(t, os.MkdirAll(repoPath, 0o755))

	err := os.WriteFile(filepath.Join(repoPath, "test.txt"), []byte("line 1\nline 2\nline 3\n"), 0o644)
	require.NoError(t, err)

	repos := make(RepoResolverMap)
	repos["myrepo"] = repoPath

	lg := zaptest.NewLogger(t)
	reader := NewLocalRepoReader(repos, lg)

	ctx := context.Background()

	t.Run("found", func(t *testing.T) {
		resp, err := reader.ResolveContent(ctx, index.ContentRequest{
			Repo: "myrepo",
			Path: "test.txt",
		})
		require.NoError(t, err)
		require.True(t, resp.Found)
		require.Equal(t, "local_clone", resp.Source)
		require.Equal(t, "line 1\nline 2\nline 3\n", resp.Content)
	})

	t.Run("line range", func(t *testing.T) {
		resp, err := reader.ResolveContent(ctx, index.ContentRequest{
			Repo:  "myrepo",
			Path:  "test.txt",
			Start: 2,
			End:   2,
		})
		require.NoError(t, err)
		require.True(t, resp.Found)
		require.Equal(t, "line 2", resp.Content)
	})

	t.Run("not found", func(t *testing.T) {
		resp, err := reader.ResolveContent(ctx, index.ContentRequest{
			Repo: "myrepo",
			Path: "missing.txt",
		})
		require.NoError(t, err)
		require.False(t, resp.Found)
	})

	t.Run("path traversal", func(t *testing.T) {
		resp, err := reader.ResolveContent(ctx, index.ContentRequest{
			Repo: "myrepo",
			Path: "../outside.txt",
		})
		require.NoError(t, err)
		require.False(t, resp.Found)
	})

	t.Run("symlink outside root", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink privileges are not guaranteed on Windows")
		}
		outside := filepath.Join(tmpDir, "outside.txt")
		require.NoError(t, os.WriteFile(outside, []byte("secret"), 0o600))
		require.NoError(t, os.Symlink(outside, filepath.Join(repoPath, "leak.txt")))

		resp, err := reader.ResolveContent(ctx, index.ContentRequest{
			Repo: "myrepo",
			Path: "leak.txt",
		})
		require.NoError(t, err)
		require.False(t, resp.Found)
	})

	t.Run("too large", func(t *testing.T) {
		large := filepath.Join(repoPath, "large.txt")
		require.NoError(t, os.WriteFile(large, make([]byte, maxContentBytes+1), 0o600))

		resp, err := reader.ResolveContent(ctx, index.ContentRequest{
			Repo: "myrepo",
			Path: "large.txt",
		})
		require.NoError(t, err)
		require.False(t, resp.Found)
	})
}
