# Ingestion

`ssingest` is the one-shot ingestion CLI. It fetches configured sources, normalizes them into documents, chunks them, embeds changed chunks, writes Postgres rows, and upserts Qdrant vectors.

Re-running ingestion is safe. Incremental state is stored in `sync_states`, and unchanged documents are skipped by body hash.

## Commands

Run all configured sources in sequence:

```bash
make ingest
# or
go run ./cmd/ssingest all
```

Run one source at a time:

```bash
make ingest-git
make ingest-gitlab
make ingest-jira
make ingest-telegram

go run ./cmd/ssingest git
go run ./cmd/ssingest gitlab
go run ./cmd/ssingest jira
go run ./cmd/ssingest telegram
```

Preview a run without indexing:

```bash
go run ./cmd/ssingest all --dry-run
go run ./cmd/ssingest gitlab --dry-run --limit 25
```

Limit indexed documents:

```bash
go run ./cmd/ssingest git --limit 100
go run ./cmd/ssingest jira --limit 50
```

Override the saved cursor for GitLab or Jira:

```bash
go run ./cmd/ssingest gitlab --since 2026-01-01T00:00:00Z
go run ./cmd/ssingest jira --since 2026-01-01T00:00:00Z
```

Rebuild one source from scratch:

```bash
go run ./cmd/ssingest git --reset git
go run ./cmd/ssingest gitlab --reset gitlab
go run ./cmd/ssingest jira --reset jira
go run ./cmd/ssingest telegram --reset telegram
```

Rebuild every configured source from scratch:

```bash
go run ./cmd/ssingest all --reset all --yes-i-mean-all
```

Skip git orphan cleanup for files removed from a repository:

```bash
go run ./cmd/ssingest git --no-prune
```

## Git

Configure repositories under `git.repos`:

```yaml
git:
  work_dir: /data/git
  token:
    env: SISYPHUS_GIT_TOKEN
  repos:
    - url: https://gitlab.example.com/group/docs.git
      repo: docs
      branch: main
      base_url: https://gitlab.example.com/group/docs/-/blob/main
      commits: true
      tags: false
      include:
        - "docs/**/*.md"
      exclude:
        - "docs/archive/**"
```

Git sources are keyed per repository:

- `git_docs:<repo>` for Markdown/content files.
- `git_commits:<repo>` for commit messages when `commits: true`.
- `git_tags:<repo>` for tags when `tags: true`.

Docs and tags have no cursor; each run walks the repo and relies on body-hash skips. Commit ingestion uses a cursor with the last processed SHA and branch.

## GitLab

Configure GitLab REST ingestion with project IDs or paths:

```yaml
gitlab:
  base_url: https://gitlab.example.com
  token:
    env: SISYPHUS_GITLAB_TOKEN
  projects:
    - ref: group/docs
    - ref: "42"
  issues: true
  merge_requests: true
  releases: true
```

GitLab ingests issues, merge requests, releases, assignees/reviewers, MR merge metadata, cross-references, and comments grouped into discussion threads. It stores independent cursors for issues, merge requests, and releases so interrupted runs can resume.

## Jira

Configure Jira with one supported auth method and a list of project objects:

```yaml
jira:
  base_url: https://jira.example.com
  email: bot@example.com
  api_token:
    env: SISYPHUS_JIRA_APITOKEN
  projects:
    - key: SUP
    - key: PLAT
```

Use `--since` to backfill from a specific RFC3339 timestamp instead of the saved cursor.

## Telegram

Configure Telegram application credentials, bot token, session storage, and chats:

```yaml
telegram:
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
```

Telegram ingestion requires an existing user ingest session. If the session is missing, `ssingest telegram` exits with `telegram not configured or ingest session missing`.

Telegram Desktop/GDPR dump JSON files can be passed to the `telegram` subcommand as positional args. Dump ingestion does not require live Telegram API credentials when only dump files are provided.

## Context Files

Use `context_files` for curated local files outside Git repositories:

```yaml
context_files:
  - name: runbooks
    root: /data/context/runbooks
    base_url: ""
    include: ["**/*.md", "**/*.txt"]
    exclude: []
    authority: high
```

Include and exclude patterns are doublestar globs relative to `root`.
