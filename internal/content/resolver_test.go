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
	reader := NewLocalRepoReader(repos, Options{Logger: lg})

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

// fakeResolver is a simple test implementation of index.ContentResolver
type fakeResolver struct {
	response index.ContentResponse
	err      error
	called   int
}

func (f *fakeResolver) ResolveContent(ctx context.Context, req index.ContentRequest) (index.ContentResponse, error) {
	f.called++
	return f.response, f.err
}

func TestChainResolverResolveContent(t *testing.T) {
	lg := zaptest.NewLogger(t)

	t.Run("first resolver found short-circuits chain", func(t *testing.T) {
		first := &fakeResolver{
			response: index.ContentResponse{
				Content: "first content",
				Source:  "first",
				Found:   true,
			},
		}
		second := &fakeResolver{
			response: index.ContentResponse{
				Content: "second content",
				Source:  "second",
				Found:   true,
			},
		}

		chain := NewChainResolver([]index.ContentResolver{first, second}, Options{Logger: lg})
		resp, err := chain.ResolveContent(context.Background(), index.ContentRequest{
			Repo: "test",
			Path: "file.txt",
		})

		require.NoError(t, err)
		require.True(t, resp.Found)
		require.Equal(t, "first content", resp.Content)
		require.Equal(t, 1, first.called)
		require.Equal(t, 0, second.called) // second should not be called
	})

	t.Run("first not found falls through to second", func(t *testing.T) {
		first := &fakeResolver{
			response: index.ContentResponse{
				Found: false,
			},
		}
		second := &fakeResolver{
			response: index.ContentResponse{
				Content: "second content",
				Source:  "second",
				Found:   true,
			},
		}

		chain := NewChainResolver([]index.ContentResolver{first, second}, Options{Logger: lg})
		resp, err := chain.ResolveContent(context.Background(), index.ContentRequest{
			Repo: "test",
			Path: "file.txt",
		})

		require.NoError(t, err)
		require.True(t, resp.Found)
		require.Equal(t, "second content", resp.Content)
		require.Equal(t, 1, first.called)
		require.Equal(t, 1, second.called)
	})

	t.Run("first resolver error skipped, second used", func(t *testing.T) {
		first := &fakeResolver{
			err: context.Canceled,
		}
		second := &fakeResolver{
			response: index.ContentResponse{
				Content: "second content",
				Source:  "second",
				Found:   true,
			},
		}

		chain := NewChainResolver([]index.ContentResolver{first, second}, Options{Logger: lg})
		resp, err := chain.ResolveContent(context.Background(), index.ContentRequest{
			Repo: "test",
			Path: "file.txt",
		})

		require.NoError(t, err) // ChainResolver returns nil error
		require.True(t, resp.Found)
		require.Equal(t, "second content", resp.Content)
		require.Equal(t, 1, first.called)
		require.Equal(t, 1, second.called)
	})

	t.Run("all resolvers not found returns not found", func(t *testing.T) {
		first := &fakeResolver{
			response: index.ContentResponse{
				Found: false,
			},
		}
		second := &fakeResolver{
			response: index.ContentResponse{
				Found: false,
			},
		}

		chain := NewChainResolver([]index.ContentResolver{first, second}, Options{Logger: lg})
		resp, err := chain.ResolveContent(context.Background(), index.ContentRequest{
			Repo: "test",
			Path: "file.txt",
		})

		require.NoError(t, err)
		require.False(t, resp.Found)
		require.Equal(t, 1, first.called)
		require.Equal(t, 1, second.called)
	})

	t.Run("all resolvers error returns not found", func(t *testing.T) {
		first := &fakeResolver{
			err: context.Canceled,
		}
		second := &fakeResolver{
			err: context.DeadlineExceeded,
		}

		chain := NewChainResolver([]index.ContentResolver{first, second}, Options{Logger: lg})
		resp, err := chain.ResolveContent(context.Background(), index.ContentRequest{
			Repo: "test",
			Path: "file.txt",
		})

		require.NoError(t, err)
		require.False(t, resp.Found)
		require.Equal(t, 1, first.called)
		require.Equal(t, 1, second.called)
	})

	t.Run("mixed errors and not-found returns not found", func(t *testing.T) {
		first := &fakeResolver{
			err: context.Canceled,
		}
		second := &fakeResolver{
			response: index.ContentResponse{
				Found: false,
			},
		}
		third := &fakeResolver{
			err: context.DeadlineExceeded,
		}

		chain := NewChainResolver([]index.ContentResolver{first, second, third}, Options{Logger: lg})
		resp, err := chain.ResolveContent(context.Background(), index.ContentRequest{
			Repo: "test",
			Path: "file.txt",
		})

		require.NoError(t, err)
		require.False(t, resp.Found)
		require.Equal(t, 1, first.called)
		require.Equal(t, 1, second.called)
		require.Equal(t, 1, third.called)
	})
}
