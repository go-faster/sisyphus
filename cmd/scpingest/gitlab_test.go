package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/go-faster/scpbot/internal/config"
)

func TestGitLabSources(t *testing.T) {
	sources := gitLabSources([]config.GitLabSource{
		{
			Root:    "/tmp/docs",
			URL:     "https://gitlab.example.com/group/docs.git",
			Repo:    "docs",
			Branch:  "main",
			BaseURL: "https://gitlab.example.com/group/docs/-/blob/main",
		},
	})

	require.Len(t, sources, 1)
	require.Equal(t, "/tmp/docs", sources[0].Root)
	require.Equal(t, "https://gitlab.example.com/group/docs.git", sources[0].URL)
	require.Equal(t, "docs", sources[0].Repo)
	require.Equal(t, "main", sources[0].Branch)
	require.Equal(t, "https://gitlab.example.com/group/docs/-/blob/main", sources[0].BaseURL)
}
