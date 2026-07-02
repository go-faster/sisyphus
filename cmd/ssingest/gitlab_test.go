package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/config"
)

func TestGitSources(t *testing.T) {
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
}
