# sisyphus

Internal support/dev assistant. Ingests knowledge sources (git repo docs and commits,
GitLab REST API issues/MRs/releases, Jira issues, Telegram support threads) into a
hybrid search index (Postgres full-text + Qdrant vectors) and answers questions via a
Telegram bot `/context` command.

## Stack

- **API**: [ogen](https://github.com/ogen-go/ogen) — OpenAPI codegen. Spec in `api/openapi.yaml`, generated into `internal/oas`.
- **DB**: [entgo/ent](https://entgo.io/) — schema in `internal/ent/schema`, generated into `internal/ent`. Postgres is the source of truth + FTS.
- **Telegram**: [gotd/td](https://github.com/gotd/td) — MTProto. User session backfills history; bot token serves `/context`.
- **App runner**: [go-faster/sdk](https://github.com/go-faster/sdk) — `app.Run` for lifecycle, logging (`zap`), metrics/traces (otel).
- **Vectors**: Qdrant. **Embeddings**: Ollama (`bge-m3`, 1024 dims).

## Architecture

```
Raw source -> Normalized Document -> Search Chunks
Postgres = source of truth + metadata + FTS
Qdrant   = vector search over chunks
```

Never store only embeddings — always keep Documents+Chunks in Postgres so we can reindex.

## Layout

```
cmd/ssapi              owns Postgres/ent + migrations; serves the HTTP API
                        (bearer-token auth on /search, /context; /health public)
cmd/ssbot              thin Telegram /context bot; talks to ssapi via internal/apiclient
cmd/ssmcp              MCP server entrypoint (Streamable HTTP or stdio); calls ssapi via internal/apiclient
cmd/ssingest           one-shot ingestion CLI: git|gitlab|jira|telegram|all subcommands,
                        --reset <src|all> (--yes-i-mean-all for all), --since, --limit, --dry-run.
                        Wires its dependencies inline (does NOT reuse internal/wire).
internal/index          SHARED CONTRACT: Document, Chunk, Chunker, Embedder, Searcher, constants. Do not add deps here.
internal/chunk/markdown heading-aware Markdown chunker (implements index.Chunker)
internal/chunk/git      git commit message / tag -> chunks (implements index.Chunker)
internal/chunk/gitlab   GitLab REST API (issues, MRs, releases) -> chunks (implements index.Chunker)
internal/chunk/jira     Jira issue -> chunks (implements index.Chunker)
internal/embed/ollama   Ollama embedder (implements index.Embedder)
internal/search/postgres FTS searcher over ent (implements index.Searcher)
internal/search/qdrant  Qdrant client + searcher (implements index.Searcher)
                        Also implements pipeline.VectorStore (Upsert + Delete by point ID).
internal/retrieval      merges Postgres+Qdrant results via Reciprocal Rank Fusion (RRF,
                        k=60), then applies authority/boost rules
internal/ingest/git     git repo content (Markdown) + commits + tags walker; local or clone/pull via git
internal/ingest/gitlab  GitLab REST API client (stdlib net/http) with pagination + cursor per resource
internal/ingest/jira    incremental Jira REST client (stdlib net/http) with sliding-window cursor
internal/ingest/telegram gotd user-session backfill -> telegram_messages -> support_requests;
                        MessageFetcher interface for testability; bootstrapPeers resolves access hashes
internal/pipeline       Pipeline.Index: idempotent doc+chunk upsert (ent) + embed (Ollama)
                        + vector Upsert/Delete (Qdrant). Per-chunk embedding skip (preserves
                        unchanged chunks' qdrant_point_id) and stale-point cleanup on changed docs.
                        VectorStore interface: Upsert + Delete.
internal/bot            gotd bot, /context handler
internal/ent            ent schema + generated code (Document, Chunk, SupportRequest,
                        TelegramMessage, SyncState)
internal/llm/openrouter OpenRouter-backed LLM answerer (chat completions) used to
                        answer /context questions from retrieved chunks (non-agentic path).
internal/agent          shared LLM tool-calling loop engine (coreLoop in core.go) used by
                        BOTH /investigate (loop.go, Report/submit_report terminal tool) and
                        the agentic /context path (internal/answer.ContextLoop); also collects
                        DiscoveredURLs from tool results (source_url/url JSON fields only —
                        never free-form body text, see "Answers & link buttons")
internal/answer         agentic /context answerer (index.RichAnswerer): AgenticAnswerer runs
                        agent.coreLoop with search_knowledge/fetch_url tools (knowledge_tools.go,
                        in-process) merged via MultiToolSource with an optional ssh-mcp-backed
                        shell sandbox (ssh_tools.go, MCP client over streamable-http). Enabled by
                        `context.agentic: true` + OpenRouter; falls back to
                        `internal/llm/openrouter.Answerer` otherwise (see wire.go)
internal/apiclient      oas.Client adapter satisfying bot/mcpserver's local Retriever
                        interface + index.Answerer over HTTP with bearer auth
internal/content        index.ContentResolver implementations backing the file-content
                        API/tool: DatabaseReader (document.body from Postgres, with git_docs/
                        git_code/git_manifest source-prefix fallback), LocalRepoReader (local
                        git clone on disk, path-traversal/symlink-escape guarded), ChainResolver
                        (tries resolvers in order, first Found wins)
internal/fetch          index.URLFetcher implementation: per-site HTTP allowlist (URL glob
                        patterns, allowed methods, credential injection, byte cap) configured
                        via FetchConfig; backs the fetch API/MCP tool
internal/mcpserver      MCP server impl (search/answer/file/fetch tools) + BearerAuthMiddleware
                        for ssmcp's optional /mcp bearer auth
internal/wire           shared wiring for cmd/ssapi and cmd/ssingest (Services + Components)
internal/oas            ogen generated code
api/openapi.yaml        OpenAPI spec (source for ogen)
deploy/                 docker-compose + configs + .env.example
```

Service routing is currently inert: retrieval's `service` boost falls back to
1.0 when `metadata.service` is absent. Add real service routing only when query
quality demands it.

## Answers & link buttons

Answers can carry actionable links rendered as Telegram inline URL buttons.

- `index.Link{Text,URL}` (`Valid()` requires an absolute http(s) URL + non-empty
  label) and `index.Answer{Text,Links}` are the shared types. `index.RichAnswerer`
  is an **opt-in** extension of `index.Answerer` (`AnswerRich` returns `index.Answer`),
  detected via type assertion — plain `Answerer.Answer` (string) still works and is
  what MCP + tests use.
- `/context` (`internal/llm/openrouter.Answerer.AnswerRich`): the answerer prompt
  tells the model to cite sources as inline Markdown links, and a `submit_answer`
  tool returns `{answer, buttons}`. Button URLs are validated **and constrained to
  the retrieved sources' `source_url`** (see `filterButtons`) so the model can't
  surface a hallucinated/off-context URL. Buttons cross the HTTP boundary as
  `ContextResponse.buttons` (oas `Link`); `internal/api` populates them,
  `internal/apiclient.AnswerQueryRich` reads them (re-validating in `fromLinks`),
  and `internal/bot` renders them via `linksMarkup` on the `/context` reply.
- `/investigate` (`internal/agent`): `Report.Links` comes from the `submit_report`
  tool's `links` param; `Report.normalize` drops invalid/duplicate links and caps
  at `maxReportLinks`. Here links may be **any** http(s) URL the agent obtained from
  tool results (dashboards, tickets), not just cited sources. The bot attaches them
  to the final report message.
- Agentic `/context` (`internal/answer.AgenticAnswerer.AnswerRich`, opt-in via
  `context.agentic: true`): same `submit_answer`/`filterButtons` contract as the
  OpenRouter answerer above, but the allowed-URL set is built from two sources —
  the seed results' `source_url` (`buildSeedMessages`) plus any URL the loop
  *discovers* while calling tools mid-conversation. Discovery is
  `agent.collectURLs` in `internal/agent/core.go`: it only extracts URLs from
  structured `"source_url"`/`"url"` JSON keys in a tool's result, **never** by
  regexing the full result text. This matters because tool results carry
  untrusted ingested content (a chunk's body, a fetched page) — a naive
  whole-text URL scan would let any link merely *mentioned* in that content
  become a clickable button, defeating the "constrained to vetted sources"
  guarantee. Keep this restriction if `collectURLs` or its call site ever change.

## API auth

The HTTP API (`cmd/ssapi`) requires a shared static bearer token (`api.auth_token`
config / `SISYPHUS_API_AUTH_TOKEN` env), enforced server-side via
`internal/api.SecurityHandler` (an ogen-generated `SecurityHandler`), and attached
client-side by `internal/apiclient`. `/health` is the only unauthenticated route.

`cmd/ssmcp`'s `/mcp` endpoint has optional bearer auth (`mcp.auth_token` config /
`SISYPHUS_MCP_AUTH_TOKEN` env), enforced by `internal/mcpserver.BearerAuthMiddleware`.
Unlike `ssapi`, an empty token does **not** fail startup — it just logs a warning and
serves `/mcp` unauthenticated. Set it in any deployment reachable from untrusted
networks.

## Config layout

Each service's own settings live in a per-service YAML section rather than as
flat top-level keys: `api.http_addr` (ssapi's server), `mcp.addr`/`mcp.auth_token`
(ssmcp), `telegram.addr` (ssbot's standalone health server — it has no other
HTTP API to attach health checks to), `agent.addr` (ssagent). The old flat
`http_addr`, `mcp_addr`, and `mcp_auth_token` top-level keys still parse for
backwards compatibility but are deprecated: using one logs a warning
(`Config.Warnings`, surfaced via `Config.LogWarnings`), and setting both the
old and new field for the same value is a hard error at `config.Load()` time.
See `internal/config/config.go`'s `resolveDeprecatedAddr`/`resolveDeprecatedSecret`.

`cmd/ssbot`'s Telegram bot is allowlist-gated and **fails closed**: `telegram.allowed_chats`
/ `allowed_user_ids` (both empty by default) must list at least one chat or user, or the
bot silently ignores every message (see `internal/bot.Bot.isAllowed`).

`proxies.*` (`ProxyConfig`) configures a per-client HTTP proxy: `git`, `gitlab`, `jira`,
`ollama`, `openrouter`, and `fetch` (the dedicated proxy for `internal/fetch` sites that
set `proxy: fetch`). A fetch site's `proxy` name is resolved twice — once in
`internal/config/config.go` (`fetchProxyURL`, used for config validation) and again in
`internal/fetch/fetcher.go` (`proxyURL`, used to actually build the site's `http.Client`).
Both switches must be kept in sync when adding a new proxy name; a name present in one but
not the other either fails validation for a working proxy or silently fetches with no
proxy at all.

`context.*` (`ContextConfig`) controls the agentic `/context` path: `agentic` (default
false) only takes effect if OpenRouter is also enabled (`wire.New` silently falls back to
the non-agentic `openrouter.Answerer`/stub otherwise — no startup warning today if an
operator sets `agentic: true` without OpenRouter configured). `ssh_mcp_url` points at an
ssh-mcp sidecar (see `deploy/sandbox/`); when unset or unreachable, sandbox/`ssh_*` tools
are unavailable and `AgenticAnswerer` tells the model so in its prompt instead of silently
letting `ssh_*` tool calls fail (`AgenticOptions.SandboxEnabled`, wired from
`sshTools != nil` in `wire.go`). `pre_search`/`pre_search_limit` seed the agent loop with
an initial retrieval — `AnswerRich` only runs this when the caller didn't already pass
results (`internal/api.Handler.Context` always retrieves first), to avoid a duplicate
query that would also drop the caller's service/source-tier filters.

## Conventions

- `internal/index` is the contract. It must stay dependency-light (stdlib + `github.com/google/uuid` only). All other packages depend on it, not on each other where avoidable.
- Implement the interfaces in `internal/index` exactly; do not change their signatures without updating this file and every implementer.
- Configuration: use struct-based options with `setDefaults()` instead of functional options (`Option func(*T)`). Pattern:
  ```go
  type FooOptions struct {
      Logger *zap.Logger
      Timeout time.Duration
  }

  func (opts *FooOptions) setDefaults() {
      if opts.Logger == nil { opts.Logger = zap.L() }
      if opts.Timeout == 0 { opts.Timeout = 30 * time.Second }
  }

  func NewFoo(required Param, opts FooOptions) *Foo {
      opts.setDefaults()
      // ...
  }
  ```
- Configuration lists are YAML sequences of objects, not comma-separated strings. Examples: `gitlab.projects: [{ref: group/docs}]`, `jira.projects: [{key: IDP}]`, `telegram.monitor_chats: [{id: -100123, username: support}]`.
- Errors: wrap with `github.com/go-faster/errors` (`errors.Wrap`). No `fmt.Errorf("...%w")`.
- `errors.Wrap(f(), "msg")` as a return statement is wrong: if `f()` returns nil, `errors.Wrap` still returns a non-nil error. Always check first: `if err := f(); err != nil { return errors.Wrap(err, "msg") }`.
- File structure: split logical sections into separate files instead of separating them with `//` comments using `--` dividers. Even if a file seems large, prefer multiple focused files over in-file section markers.
- Logging: `*zap.Logger` passed in; no global loggers, no `log` package.
- Content hashing: `internal/index.Hash` (sha256 of normalized text). Skip re-embedding when hash is unchanged.
- IDs: `github.com/google/uuid`.
- Document identity: unique on `(source, source_id)` — not `body_hash`. Re-ingesting the
  same `source_id` with changed content updates the existing row and its chunks in place
  (see `internal/pipeline.Pipeline.Index`); it never creates a duplicate document.

## Codegen

- ent: `go generate ./internal/ent/...` (runs `ent generate`).
- ogen: `go generate ./internal/oas/...`.
- Commit generated code in a **separate commit** from the schema/spec that produced it.

### Schema migrations

`internal/ent/schema` is the single source of truth for the DB schema. Versioned SQL
migration files live in `internal/ent/migrate/migrations/` and are applied at runtime
by the hand-written `Runner` in `internal/ent/migrate/runner.go` (tracked via a
`schema_migrations` table). Only `ssapi` runs migrations
(`wire.NewOptions.RunMigrations: true`); `ssingest` connects without migrating (still holds its own DB connection via `wire.NewServices`), and `ssbot`/`ssmcp` don't touch the schema at all — they hold no DB connection whatsoever, only an HTTP client to `ssapi`. This ensures schema changes apply exactly once per deploy instead of racing across
every process/replica sharing the database.

After changing `internal/ent/schema`, generate the next migration file by diffing the
ent schema against a throwaway Postgres container (requires a local Docker daemon,
nothing else running):

```
make migrate-diff NAME=add_foo_column
```

This uses ent's `sql/versioned-migration` feature (`internal/ent/migrate/gen`) — it
spins up a scratch postgres via `testcontainers-go`, replays the existing migration
files against it, diffs the result against the ent schema, writes a new file, updates
`migrations/atlas.sum`, and tears the container down. Do not hand-edit `atlas.sum`.

Some DDL can't be expressed in the ent schema (e.g. `00002_fts.sql`'s
`GENERATED ALWAYS AS (...) STORED` tsvector column — ent only supports plain
`DEFAULT`/`DefaultExpr`, which don't recompute on `UPDATE`). Those migrations are
still hand-written directly in `migrations/`; run `make migrate-diff` afterward to
refresh `atlas.sum` (it should produce no new file, since the extra column/index isn't
declared in the ent schema).

Migration files must not contain more than the forward migration: the runner execs the
entire file as one blob with no down/rollback support, and stray SQL after a `-- +goose
Down`-style comment will actually execute.

## Build / test

- Format: `golangci-lint fmt ./...` (do not hand-format).
- Lint: `golangci-lint run --fix ./...` (`--fix` can automatically fix some issues).
- Test: `make test` (or `make test_fast` = `go test ./...`).
- Tests must be hermetic, fast (no real sleeps), non-flaky, cross-platform. DB-backed tests use testcontainers or are skipped when no DB is available — convention: skip when `SISYPHUS_TEST_DB` (postgres DSN) is unset.

## Ingestion

`make ingest` (= `go run ./cmd/ssingest all`) runs incremental backfills for every
configured source. Per-source: `make ingest-git`, `make ingest-gitlab`, `make ingest-jira`,
`make ingest-telegram`.

Each source (or per-repo for git, per-resource type for gitlab REST) has a `SyncState` row
in ent: `source`, `last_synced_at`, `last_cursor` (opaque JSON), `status`, `error`,
`document_count`. The CLI reads the cursor before the run and writes it back per batch
(jira, gitlab pagination) or per repo (git commits) so a partial run resumes.

**Git ingestion** (`make ingest-git`):
- Per-repo sources keyed `git_docs:<repo>` (Markdown content), `git_commits:<repo>` (commit messages),
  and `git_tags:<repo>` (tags, opt-in via `tags: true`).
- Docs and tags sources have no cursor; re-walk and rely on pipeline body-hash skip to avoid re-embedding.
- Commits source uses cursor `{last_sha, branch}` to walk incrementally from HEAD backwards.
- Tags: annotated tags use the tag message/tagger; lightweight tags fall back to the
  target commit's subject/author.

**GitLab REST API** (`make ingest-gitlab`):
- Per-resource-type sources: `gitlab_issue`, `gitlab_mr`, `gitlab_release`.
- Pagination loop with cursor `{updated_after}` (RFC3339). Issues and MRs sorted by `updated_at` asc;
  releases filtered client-side.
- Per-page fetch, limit honored, cursor advanced to max `updated_at` (or `released_at` for releases).
- Issues/MRs also carry assignees, and MRs carry reviewers, merge metadata (merged_at/by,
  merge_commit_sha, source/target branch, draft), and cross-references (`closes`/`relates_to`
  links via the issue links / MR closes_issues endpoints — fetched best-effort, non-fatal on error
  since they can be edition/permission-gated).
- Comments are fetched via the discussions endpoint (not flat notes) and grouped into threads,
  preserving resolved state; trivial notes are filtered per-note, empty threads dropped.
- No code diffs, no wiki, no CI/pipeline status, no merge-commit ingestion (by design).

**Jira** (`make ingest-jira`):
- Single source `jira`; incremental via cursor `{last_updated, start_at}`.
- Respects `--since` to override `last_updated`.

**Telegram** (`make ingest-telegram`):
- Single source `telegram`; cursor `{per_chat}` tracks per-chat state.
- `ssingest telegram [dump.json ...]` additionally ingests Telegram Desktop /
  GDPR chat export JSON files passed as positional args (one file per chat:
  top-level `id`/`name`/`type`/`messages`, `internal/ingest/telegram/dump.go`'s
  `Dump` type). Runs independently of the live gotd session — passing only
  dump file args (with no `app_id`/`app_hash`/`ingest_session` configured) is
  enough to ingest dumps with no Telegram API credentials. Dumps are one-shot
  exports with no pagination cursor: each run re-walks the given file(s) and
  relies on the `telegram_messages`/`support_requests` upserts and pipeline
  body-hash skip to stay idempotent. Service messages (joins/pins/...) and
  entries with no extractable text are skipped. `ssingest all` does not take
  dump file args, so dump ingestion must be run via the `telegram` subcommand
  directly.

`--reset <src|all>` wipes the source end-to-end: in one ent Tx it deletes
`documents`, `chunks`, and `SyncState` for that source (chunk IDs are captured
pre-delete), commits, then `qdrant.Delete` removes the freed point IDs. `--reset
all` refuses without `--yes-i-mean-all`. For git, resetting "all" also resets per-repo
docs and commits sources.

`--since <RFC3339>` overrides cursors (Jira `LastUpdated`, GitLab `UpdatedAfter`).
`--limit <int>` caps docs per source. `--dry-run` fetches and logs counts without indexing.

## Run

`docker compose -f deploy/docker-compose.yml up` starts postgres + qdrant + ollama + the app.
Config via env (see `deploy/.env.example`).
