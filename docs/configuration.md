# Configuration

Configuration is loaded from `SISYPHUS_CONFIG`, usually `deploy/config.yaml` copied from `deploy/config.example.yaml`.

Secrets can be literal values, environment variable references, or file references. The example config uses environment variables such as `SISYPHUS_DATABASE_DSN`, `SISYPHUS_API_AUTH_TOKEN`, `SISYPHUS_GITLAB_TOKEN`, `SISYPHUS_JIRA_APITOKEN`, and `SISYPHUS_TELEGRAM_BOT_TOKEN`.

## Services

- `ssapi`: HTTP API server. Owns Postgres access and runs schema migrations. Serves `/health`, `/search`, `/context`, file/fetch APIs, and optional GitLab/Jira webhooks.
- `ssingest`: one-shot ingestion CLI. Reads configured sources and writes documents/chunks/vectors.
- `ssbot`: Telegram bot. Handles `/context` and, when configured, `/investigate`.
- `ssmcp`: MCP server exposing search, answer, file, and fetch tools over streamable HTTP or stdio.
- `ssagent`: investigation service used by `ssbot` for `/investigate`, backed by an MCP gateway.

## Core Config

```yaml
database:
  dsn:
    env: SISYPHUS_DATABASE_DSN

api:
  http_addr: :8080
  base_url: http://ssapi:8080
  auth_token:
    env: SISYPHUS_API_AUTH_TOKEN

mcp:
  addr: :8081
  auth_token:
    env: SISYPHUS_MCP_AUTH_TOKEN

qdrant:
  addr: qdrant:6334
  collection: corp_chunks

ollama:
  url: http://ollama:11434

embed:
  provider: ollama
  model: bge-m3
  dim: 1024
```

`database.dsn` and `api.auth_token` are required for `ssapi`. `mcp.auth_token` is optional, but should be set for any deployment reachable from untrusted networks.

## Answering Config

```yaml
openrouter:
  api_key:
    env: SISYPHUS_OPENROUTER_API_KEY
  model: openai/gpt-4o-mini

context:
  agentic: false
  max_iterations: 6
  timeout_seconds: 180
  max_answer_chars: 2000
  ssh_mcp_url: ""
  ssh_mcp_headers: {}
  sandbox_machine: sandbox
  pre_search: true
  pre_search_limit: 12
```

`context.agentic` enables the agentic `/context` path only when OpenRouter is configured. `ssh_mcp_url` adds optional sandbox tools; when unset or unreachable, answers still work without sandbox tools.

## Telegram Config

```yaml
telegram:
  addr: :8083
  app_id: 12345
  app_hash:
    env: SISYPHUS_TELEGRAM_APP_HASH
  bot_token:
    env: SISYPHUS_TELEGRAM_BOT_TOKEN
  session_dir: /data/scp/session
  monitor_chats:
    - id: -1001234567890
      username: support-chat
  ingest_session: support-ingest
  allowed_chats: []
  allowed_user_ids: []
```

`ssbot` is allowlist-gated and fails closed: if both `allowed_chats` and `allowed_user_ids` are empty, the bot ignores every message. Telegram ingestion also requires a valid user session named by `ingest_session`.

## Agent Config

```yaml
agent:
  addr: :8082
  base_url: http://ssagent:8082
  auth_token:
    env: SISYPHUS_AGENT_AUTH_TOKEN
  model: openai/gpt-4o
  max_tool_iterations: 8
  request_timeout_seconds: 180
  gateway_url: http://mcpgateway:8090/mcp
  max_report_chars: 1500
  max_concurrent: 4
  max_body_bytes: 65536
```

Leave `agent.base_url` empty to disable `/investigate` in `ssbot`.

`agent.max_concurrent` caps how many `/investigate` requests run at once — each
holds an LLM tool-calling loop open for up to `request_timeout_seconds`, so an
unbounded fan-out would hold that many goroutines and bill that much LLM spend
simultaneously. Requests beyond the cap get `429 Too Many Requests`.
`agent.max_body_bytes` caps the POST body size for `/investigate`.

## Source Config

Source-specific examples are in [ingestion.md](ingestion.md):

- `git.repos` for Git docs, commits, and tags.
- `gitlab.projects` for GitLab issues, merge requests, and releases.
- `jira.projects` for Jira issue ingestion.
- `telegram.monitor_chats` for Telegram support chat ingestion.
- `context_files` for curated local files.

## Proxies

```yaml
proxies:
  git:
    env: SISYPHUS_GIT_PROXY
  gitlab:
    env: SISYPHUS_GITLAB_PROXY
  jira:
    env: SISYPHUS_JIRA_PROXY
  ollama:
    env: SISYPHUS_OLLAMA_PROXY
  openrouter:
    env: SISYPHUS_OPENROUTER_PROXY
```

Proxy settings are per client. Fetch sites can also opt into the dedicated `fetch` proxy when configured.
