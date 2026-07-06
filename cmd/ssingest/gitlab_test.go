package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/config"
)

func TestGitSources(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		sources := gitSources([]config.GitSource{
			{
				Root:    "/tmp/docs",
				URL:     "https://github.com/example/docs.git",
				Repo:    "docs",
				Branch:  "main",
				BaseURL: "https://github.com/example/docs",
				Include: []string{"**/*.md"},
				Exclude: []string{"CLAUDE.md"},
				Commits: true,
			},
		})

		require.Len(t, sources, 1)
		require.Equal(t, "/tmp/docs", sources[0].Root)
		require.Equal(t, "https://github.com/example/docs.git", sources[0].URL)
		require.Equal(t, "docs", sources[0].Repo)
		require.Equal(t, "main", sources[0].Branch)
		require.Equal(t, "https://github.com/example/docs", sources[0].BaseURL)
		require.Equal(t, []string{"**/*.md"}, sources[0].Include)
		require.Equal(t, []string{"CLAUDE.md"}, sources[0].Exclude)
		require.True(t, sources[0].Commits)
	})

	t.Run("manifests and code", func(t *testing.T) {
		sources := gitSources([]config.GitSource{
			{
				Root:            "/tmp/repo",
				Repo:            "platform/repo",
				Manifests:       true,
				Code:            true,
				ManifestExclude: []string{"_out/**"},
				CodeInclude:     []string{"src/**"},
				CodeExclude:     []string{"**/*_test.go"},
			},
		})

		require.Len(t, sources, 1)
		require.True(t, sources[0].Manifests)
		require.True(t, sources[0].Code)
		require.Equal(t, []string{"_out/**"}, sources[0].ManifestExclude)
		require.Equal(t, []string{"src/**"}, sources[0].CodeInclude)
		require.Equal(t, []string{"**/*_test.go"}, sources[0].CodeExclude)
	})
}
