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
qdrant_addr: qdrant:6334
embed_dim: 512
api:
  http_addr: :9090
  base_url: http://ssapi:8080
  auth_token:
    value: test-token
mcp:
  addr: :9092
  auth_token:
    value: mcp-token
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
fetch:
  sites:
    - name: gitlab-internal
      url_patterns:
        - https://gitlab.example.com/**
      methods: [GET, POST]
      proxy: git
      credentials:
        type: bearer
        token_env: TEST_SISYPHUS_FETCH_TOKEN
      max_bytes: 524288
      timeout: 30s
telegram:
  addr: :9091
  app_id: 123
  session_dir: /tmp/sisyphus-session
  monitor_chats:
    - id: -100123
      username: support-chat
      limit: 50
openrouter:
  model: test-model
agent:
  addr: :8082
  base_url: http://ssagent:8082
  auth_token:
    env: TEST_SISYPHUS_AGENT_AUTH_TOKEN
  model: openai/gpt-4o
  max_tool_iterations: 8
  request_timeout_seconds: 180
  gateway_url: http://mcpgateway:8090/mcp
`)
	t.Setenv("SISYPHUS_CONFIG", path)
	t.Setenv("TEST_SISYPHUS_GIT_TOKEN", "git-token")
	t.Setenv("TEST_SISYPHUS_GITLAB_TOKEN", "gitlab-token")
	t.Setenv("TEST_SISYPHUS_GIT_PROXY", "http://127.0.0.1:8083")
	t.Setenv("TEST_SISYPHUS_AGENT_AUTH_TOKEN", "agent-token")
	t.Setenv("TEST_SISYPHUS_FETCH_TOKEN", "fetch-token")

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, "postgres://user:pass@localhost/sisyphus?sslmode=disable", cfg.DatabaseDSN)
	require.Equal(t, ":9090", cfg.API.HTTPAddr)
	require.Equal(t, ":9092", cfg.MCP.Addr)
	require.Equal(t, "mcp-token", cfg.MCP.AuthToken)
	require.Equal(t, ":9091", cfg.Telegram.Addr)
	require.Equal(t, "qdrant:6334", cfg.QdrantAddr)
	require.Equal(t, 512, cfg.EmbedDim)
	require.Equal(t, 123, cfg.Telegram.AppID)
	require.Equal(t, "/tmp/sisyphus-session", cfg.Telegram.SessionDir)
	require.Equal(t, "test-model", cfg.OpenRouter.Model)
	require.Equal(t, "corp_chunks", cfg.QdrantCollection)
	require.Equal(t, "http://ssapi:8080", cfg.API.BaseURL)
	require.Equal(t, "test-token", cfg.API.AuthToken)
	require.Contains(t, cfg.Warnings, "fetch site gitlab-internal allows write method POST; prefer read-only methods unless explicitly required")

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
	require.Len(t, cfg.Fetch.Sites, 1)
	require.Equal(t, "gitlab-internal", cfg.Fetch.Sites[0].Name)
	require.Equal(t, []string{"https://gitlab.example.com/**"}, cfg.Fetch.Sites[0].URLPatterns)
	require.Equal(t, []string{"GET", "POST"}, cfg.Fetch.Sites[0].Methods)
	require.Equal(t, "git", cfg.Fetch.Sites[0].Proxy)
	require.Equal(t, int64(524288), cfg.Fetch.Sites[0].MaxBytes)
	require.Equal(t, "bearer", cfg.Fetch.Sites[0].Credentials.Type)
	require.Equal(t, "fetch-token", cfg.Fetch.Sites[0].Credentials.Token)

	require.Equal(t, ":8082", cfg.Agent.Addr)
	require.Equal(t, "http://ssagent:8082", cfg.Agent.BaseURL)
	require.Equal(t, "agent-token", cfg.Agent.AuthToken)
	require.Equal(t, "openai/gpt-4o", cfg.Agent.Model)
	require.Equal(t, 8, cfg.Agent.MaxToolIterations)
	require.Equal(t, 180, cfg.Agent.RequestTimeoutSeconds)
	require.Equal(t, "http://mcpgateway:8090/mcp", cfg.Agent.GatewayURL)
}

func TestLoadProxyFetch(t *testing.T) {
	clearEnv(t)

	path := writeConfig(t, `database_dsn:
  value: postgres://user:pass@localhost/sisyphus?sslmode=disable
qdrant_addr: qdrant:6334
proxies:
  fetch:
    value: http://fetch-proxy:8080
fetch:
  sites:
    - name: fetch-allowed
      url_patterns:
        - https://fetch.example.com/**
      proxy: fetch
`)
	t.Setenv("SISYPHUS_CONFIG", path)

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, "http://fetch-proxy:8080", cfg.Proxies.Fetch)
	require.Len(t, cfg.Fetch.Sites, 1)
	require.Equal(t, "fetch-allowed", cfg.Fetch.Sites[0].Name)
	require.Equal(t, "fetch", cfg.Fetch.Sites[0].Proxy)
}

func TestLoadSecretEnv(t *testing.T) {
	clearEnv(t)

	path := writeConfig(t, `database_dsn:
  env: TEST_SISYPHUS_DATABASE_DSN
openrouter:
  api_key:
    env: TEST_SISYPHUS_OPENROUTER_API_KEY
jira:
  username:
    env: TEST_SISYPHUS_JIRA_USERNAME
  password:
    env: TEST_SISYPHUS_JIRA_PASSWORD
`)
	t.Setenv("SISYPHUS_CONFIG", path)
	t.Setenv("TEST_SISYPHUS_DATABASE_DSN", "env-dsn")
	t.Setenv("TEST_SISYPHUS_OPENROUTER_API_KEY", "env-key")
	t.Setenv("TEST_SISYPHUS_JIRA_USERNAME", "jira-user")
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
		"TEST_SISYPHUS_MCP_AUTH_TOKEN",
		"TEST_SISYPHUS_AGENT_AUTH_TOKEN",
		"TEST_SISYPHUS_FETCH_TOKEN",
	} {
		t.Setenv(key, "")
	}
}

func TestMCPAuthToken(t *testing.T) {
	clearEnv(t)

	path := writeConfig(t, `database_dsn:
  value: postgres://user:pass@localhost/sisyphus?sslmode=disable
mcp:
  auth_token:
    env: TEST_SISYPHUS_MCP_AUTH_TOKEN
`)
	t.Setenv("SISYPHUS_CONFIG", path)
	t.Setenv("TEST_SISYPHUS_MCP_AUTH_TOKEN", "mcp-test-token")

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, "mcp-test-token", cfg.MCP.AuthToken)
}

func TestMCPAuthTokenEmptyWhenNotConfigured(t *testing.T) {
	clearEnv(t)

	path := writeConfig(t, `database_dsn:
  value: postgres://user:pass@localhost/sisyphus?sslmode=disable
`)
	t.Setenv("SISYPHUS_CONFIG", path)

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, "", cfg.MCP.AuthToken)
}

func TestDeprecatedHTTPAddrWarns(t *testing.T) {
	clearEnv(t)

	path := writeConfig(t, `database_dsn:
  value: postgres://user:pass@localhost/sisyphus?sslmode=disable
http_addr: :7070
`)
	t.Setenv("SISYPHUS_CONFIG", path)

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, ":7070", cfg.API.HTTPAddr)
	require.Contains(t, cfg.Warnings, "http_addr is deprecated, use api.http_addr instead")
}

func TestDeprecatedHTTPAddrConflictsWithNewField(t *testing.T) {
	clearEnv(t)

	path := writeConfig(t, `database_dsn:
  value: postgres://user:pass@localhost/sisyphus?sslmode=disable
http_addr: :7070
api:
  http_addr: :7071
`)
	t.Setenv("SISYPHUS_CONFIG", path)

	_, err := Load()
	require.Error(t, err)
}

func TestDeprecatedMCPAddrWarns(t *testing.T) {
	clearEnv(t)

	path := writeConfig(t, `database_dsn:
  value: postgres://user:pass@localhost/sisyphus?sslmode=disable
mcp_addr: :7072
`)
	t.Setenv("SISYPHUS_CONFIG", path)

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, ":7072", cfg.MCP.Addr)
	require.Contains(t, cfg.Warnings, "mcp_addr is deprecated, use mcp.addr instead")
}

func TestDeprecatedMCPAddrConflictsWithNewField(t *testing.T) {
	clearEnv(t)

	path := writeConfig(t, `database_dsn:
  value: postgres://user:pass@localhost/sisyphus?sslmode=disable
mcp_addr: :7072
mcp:
  addr: :7073
`)
	t.Setenv("SISYPHUS_CONFIG", path)

	_, err := Load()
	require.Error(t, err)
}

func TestDeprecatedMCPAuthTokenConflictsWithNewField(t *testing.T) {
	clearEnv(t)

	path := writeConfig(t, `database_dsn:
  value: postgres://user:pass@localhost/sisyphus?sslmode=disable
mcp_auth_token:
  value: old-token
mcp:
  auth_token:
    value: new-token
`)
	t.Setenv("SISYPHUS_CONFIG", path)

	_, err := Load()
	require.Error(t, err)
}
