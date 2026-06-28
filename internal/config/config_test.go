package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadYAML(t *testing.T) {
	clearEnv(t)

	path := writeConfig(t, `database_dsn:
  value: postgres://user:pass@localhost/scpbot?sslmode=disable
http_addr: :9090
qdrant_addr: qdrant:6334
embed_dim: 512
telegram:
  app_id: 123
  session_dir: /tmp/scpbot-session
openrouter:
  model: test-model
`)
	t.Setenv("SCPBOT_CONFIG", path)

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, "postgres://user:pass@localhost/scpbot?sslmode=disable", cfg.DatabaseDSN)
	require.Equal(t, ":9090", cfg.HTTPAddr)
	require.Equal(t, "qdrant:6334", cfg.QdrantAddr)
	require.Equal(t, 512, cfg.EmbedDim)
	require.Equal(t, 123, cfg.Telegram.AppID)
	require.Equal(t, "/tmp/scpbot-session", cfg.Telegram.SessionDir)
	require.Equal(t, "test-model", cfg.OpenRouter.Model)
	require.Equal(t, "corp_chunks", cfg.QdrantCollection)
}

func TestLoadSecretEnv(t *testing.T) {
	clearEnv(t)

	path := writeConfig(t, `database_dsn:
  env: TEST_SCPBOT_DATABASE_DSN
openrouter:
  api_key:
    env: TEST_SCPBOT_OPENROUTER_API_KEY
`)
	t.Setenv("SCPBOT_CONFIG", path)
	t.Setenv("TEST_SCPBOT_DATABASE_DSN", "env-dsn")
	t.Setenv("TEST_SCPBOT_OPENROUTER_API_KEY", "env-key")

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, "env-dsn", cfg.DatabaseDSN)
	require.Equal(t, "env-key", cfg.OpenRouter.APIKey)
}

func TestLoadSecretFile(t *testing.T) {
	clearEnv(t)

	dir := t.TempDir()
	secretPath := filepath.Join(dir, "database_dsn")
	require.NoError(t, os.WriteFile(secretPath, []byte("file-dsn\n"), 0o600))
	configPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`database_dsn:
  file: database_dsn
`), 0o600))
	t.Setenv("SCPBOT_CONFIG", configPath)

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, "file-dsn", cfg.DatabaseDSN)
}

func TestLoadRequiresDatabaseDSN(t *testing.T) {
	clearEnv(t)

	_, err := Load()
	require.Error(t, err)
}

func writeConfig(t *testing.T, data string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
	return path
}

func clearEnv(t *testing.T) {
	t.Helper()

	for _, key := range []string{
		"SCPBOT_CONFIG",
		"TEST_SCPBOT_DATABASE_DSN",
		"TEST_SCPBOT_OPENROUTER_API_KEY",
	} {
		t.Setenv(key, "")
	}
}
