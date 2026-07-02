# scpbot

Internal support/dev assistant that ingests knowledge sources into Postgres full-text search and Qdrant vectors, then answers questions through the Telegram bot or MCP server.

Sources supported by `scpingest`:

- Git repository Markdown docs and optional commit messages.
- GitLab REST API issues, merge requests, and releases.
- Jira issues.
- Telegram support chats.

## Configuration

Copy `deploy/config.example.yaml` to `deploy/config.yaml` or provide your own config file with `SCPBOT_CONFIG`:

```bash
export SCPBOT_CONFIG=deploy/config.yaml
```

`database_dsn` is required. Secrets can be literal values, environment variable references, or file references. The example config uses environment variables such as `SCPBOT_DATABASE_DSN`, `SCPBOT_GITLAB_TOKEN`, `SCPBOT_JIRA_PASSWORD`, and `SCPBOT_TELEGRAM_BOT_TOKEN`.

For local dependencies, start Docker Compose and pull the embedding model once:

```bash
docker compose -f deploy/docker-compose.yml up -d postgres qdrant ollama
docker compose -f deploy/docker-compose.yml exec ollama ollama pull bge-m3
```

## Ingest Commands

Run all configured sources in sequence:

```bash
make ingest
# or
go run ./cmd/scpingest all
```

Run one source at a time:

```bash
make ingest-git
make ingest-gitlab
make ingest-jira
make ingest-telegram
```

The same commands can be run directly:

```bash
go run ./cmd/scpingest git
go run ./cmd/scpingest gitlab
go run ./cmd/scpingest jira
go run ./cmd/scpingest telegram
```

## Ingest Examples

Preview what would be fetched without indexing documents:

```bash
go run ./cmd/scpingest all --dry-run
go run ./cmd/scpingest gitlab --dry-run --limit 25
```

Limit a run to a fixed number of documents per source:

```bash
go run ./cmd/scpingest git --limit 100
go run ./cmd/scpingest jira --limit 50
```

Override the saved incremental cursor for GitLab or Jira:

```bash
go run ./cmd/scpingest gitlab --since 2026-01-01T00:00:00Z
go run ./cmd/scpingest jira --since 2026-01-01T00:00:00Z
```

Rebuild one source from scratch. This deletes that source's documents, chunks, sync state, and vector points before ingesting again:

```bash
go run ./cmd/scpingest git --reset git
go run ./cmd/scpingest gitlab --reset gitlab
go run ./cmd/scpingest jira --reset jira
go run ./cmd/scpingest telegram --reset telegram
```

Rebuild every configured source from scratch:

```bash
go run ./cmd/scpingest all --reset all --yes-i-mean-all
```

Skip git orphan cleanup for files removed from a repository:

```bash
go run ./cmd/scpingest git --no-prune
```

## Git Ingestion

Configure repositories under `git.repos`:

```yaml
git:
  work_dir: /data/git
  token:
    env: SCPBOT_GIT_TOKEN
  repos:
    - url: https://gitlab.example.com/group/docs.git
      repo: docs
      branch: main
      base_url: https://gitlab.example.com/group/docs/-/blob/main
      commits: true
      include:
        - "docs/**/*.md"
      exclude:
        - "docs/archive/**"
```

Docs and commits are tracked as separate sources per repository: `git_docs:<repo>` and `git_commits:<repo>`. Commit ingestion is disabled unless `commits: true` is set.

## GitLab Ingestion

Configure GitLab REST ingestion with project IDs or paths:

```yaml
gitlab:
  base_url: https://gitlab.example.com
  token:
    env: SCPBOT_GITLAB_TOKEN
  projects: "group/docs,42"
  issues: true
  merge_requests: true
  releases: true
```

GitLab stores independent cursors for issues, merge requests, and releases so interrupted runs can resume from the last processed update time.

## Jira Ingestion

Configure Jira with one supported auth method and a CSV list of projects:

```yaml
jira:
  base_url: https://jira.example.com
  email: bot@example.com
  api_token:
    env: SCPBOT_JIRA_APITOKEN
  projects: "SUP,PLAT"
```

Use `--since` to backfill from a specific RFC3339 timestamp instead of the saved cursor.

## Telegram Ingestion

Configure Telegram application credentials, bot token, session storage, and chats:

```yaml
telegram:
  app_id: 12345
  app_hash:
    env: SCPBOT_TELEGRAM_APP_HASH
  bot_token:
    env: SCPBOT_TELEGRAM_BOT_TOKEN
  session_dir: /data/scp/session
  monitor_chats: "support-chat,another-chat"
  ingest_session: support-ingest
```

Telegram ingestion requires an existing user ingest session. If the session is missing, `scpingest telegram` exits with `telegram not configured or ingest session missing`.

## Development

Useful targets:

```bash
make test_fast
make test
make fmt
make lint
make codegen
```
