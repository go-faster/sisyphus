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
  value: postgres://user:pass@localhost/sisyphus?sslmode=disable
http_addr: :9090
qdrant_addr: qdrant:6334
embed_dim: 512
git:
  work_dir: /tmp/git
  token:
    env: TEST_SISYPHUS_GIT_TOKEN
  repos:
    - root: /tmp/docs
      url: https://gitlab.example.com/group/docs.git
      repo: docs
      branch: main
      base_url: https://gitlab.example.com/group/docs/-/blob/main
      commits: true
      exclude:
        - CLAUDE.md
gitlab:
  base_url: https://gitlab.example.com
  token:
    env: TEST_SISYPHUS_GITLAB_TOKEN
  projects:
    - ref: group/docs
    - ref: "42"
  issues: true
  merge_requests: true
  releases: true
jira:
  projects:
    - key: TEST
proxies:
  git:
    env: TEST_SISYPHUS_GIT_PROXY
  jira:
    value: http://127.0.0.1:8080
  ollama:
    value: http://127.0.0.1:8081
  openrouter:
    value: http://127.0.0.1:8082
telegram:
  app_id: 123
  session_dir: /tmp/sisyphus-session
  monitor_chats:
    - id: -100123
      username: support-chat
      limit: 50
openrouter:
  model: test-model
`)
	t.Setenv("SISYPHUS_CONFIG", path)
	t.Setenv("TEST_SISYPHUS_GIT_TOKEN", "git-token")
	t.Setenv("TEST_SISYPHUS_GITLAB_TOKEN", "gitlab-token")
	t.Setenv("TEST_SISYPHUS_GIT_PROXY", "http://127.0.0.1:8083")

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, "postgres://user:pass@localhost/sisyphus?sslmode=disable", cfg.DatabaseDSN)
	require.Equal(t, ":9090", cfg.HTTPAddr)
	require.Equal(t, "qdrant:6334", cfg.QdrantAddr)
	require.Equal(t, 512, cfg.EmbedDim)
	require.Equal(t, 123, cfg.Telegram.AppID)
	require.Equal(t, "/tmp/sisyphus-session", cfg.Telegram.SessionDir)
	require.Equal(t, "test-model", cfg.OpenRouter.Model)
	require.Equal(t, "corp_chunks", cfg.QdrantCollection)

	// git: repository content + commits
	require.Equal(t, "/tmp/git", cfg.Git.WorkDir)
	require.Equal(t, "git-token", cfg.Git.Token)
	require.Len(t, cfg.Git.Repos, 1)
	require.Equal(t, "/tmp/docs", cfg.Git.Repos[0].Root)
	require.Equal(t, "https://gitlab.example.com/group/docs.git", cfg.Git.Repos[0].URL)
	require.Equal(t, "docs", cfg.Git.Repos[0].Repo)
	require.Equal(t, "main", cfg.Git.Repos[0].Branch)
	require.Equal(t, "https://gitlab.example.com/group/docs/-/blob/main", cfg.Git.Repos[0].BaseURL)
	require.True(t, cfg.Git.Repos[0].Commits)
	require.Equal(t, []string{"CLAUDE.md"}, cfg.Git.Repos[0].Exclude)

	// gitlab: REST API
	require.Equal(t, "https://gitlab.example.com", cfg.GitLab.BaseURL)
	require.Equal(t, "gitlab-token", cfg.GitLab.Token)
	require.Equal(t, []GitLabProject{{Ref: "group/docs"}, {Ref: "42"}}, cfg.GitLab.Projects)
	require.True(t, cfg.GitLab.Issues)
	require.True(t, cfg.GitLab.MergeRequests)
	require.True(t, cfg.GitLab.Releases)
	require.Equal(t, []JiraProject{{Key: "TEST"}}, cfg.Jira.Projects)
	require.Equal(t, []TelegramChat{{ID: -100123, Username: "support-chat", Limit: 50}}, cfg.Telegram.MonitorChats)

	require.Equal(t, "http://127.0.0.1:8083", cfg.Proxies.Git)
	require.Equal(t, "http://127.0.0.1:8080", cfg.Proxies.Jira)
	require.Equal(t, "http://127.0.0.1:8081", cfg.Proxies.Ollama)
	require.Equal(t, "http://127.0.0.1:8082", cfg.Proxies.OpenRouter)
}

func TestLoadSecretEnv(t *testing.T) {
	clearEnv(t)

	path := writeConfig(t, `database_dsn:
  env: TEST_SISYPHUS_DATABASE_DSN
openrouter:
  api_key:
    env: TEST_SISYPHUS_OPENROUTER_API_KEY
jira:
  username: jira-user
  password:
    env: TEST_SISYPHUS_JIRA_PASSWORD
`)
	t.Setenv("SISYPHUS_CONFIG", path)
	t.Setenv("TEST_SISYPHUS_DATABASE_DSN", "env-dsn")
	t.Setenv("TEST_SISYPHUS_OPENROUTER_API_KEY", "env-key")
	t.Setenv("TEST_SISYPHUS_JIRA_PASSWORD", "jira-password")

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, "env-dsn", cfg.DatabaseDSN)
	require.Equal(t, "env-key", cfg.OpenRouter.APIKey)
	require.Equal(t, "jira-user", cfg.Jira.Username)
	require.Equal(t, "jira-password", cfg.Jira.Password)
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
	t.Setenv("SISYPHUS_CONFIG", configPath)

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
		"SISYPHUS_CONFIG",
		"TEST_SISYPHUS_DATABASE_DSN",
		"TEST_SISYPHUS_GIT_TOKEN",
		"TEST_SISYPHUS_GIT_PROXY",
		"TEST_SISYPHUS_GITLAB_TOKEN",
		"TEST_SISYPHUS_OPENROUTER_API_KEY",
		"TEST_SISYPHUS_JIRA_PASSWORD",
	} {
		t.Setenv(key, "")
	}
}
