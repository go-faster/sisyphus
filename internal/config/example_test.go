package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The shipped example is the config every operator starts from: it must load.
func TestLoadExampleConfig(t *testing.T) {
	clearEnv(t)
	t.Setenv("SISYPHUS_CONFIG", filepath.Join("..", "..", "deploy", "config.example.yaml"))
	t.Setenv("SISYPHUS_DATABASE_DSN", "postgres://example/db?sslmode=disable")

	cfg, err := Load()
	require.NoError(t, err)
	require.NotEmpty(t, cfg.QdrantCollection)
	require.NotEmpty(t, cfg.Fetch.Sites)
}
