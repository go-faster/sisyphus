# scpbot

Internal support/dev assistant. Ingests knowledge sources (GitLab Markdown docs,
Jira issues, Telegram support threads) into a hybrid search index
(Postgres full-text + Qdrant vectors) and answers questions via a Telegram bot
`/context` command.

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
cmd/scpbot              main; wires everything via go-faster/sdk app.Run
cmd/scpmcp              MCP server entrypoint (Streamable HTTP or stdio)
cmd/scpingest           one-shot ingestion CLI: gitlab|jira|telegram|all subcommands,
                        --reset <src|all> (--yes-i-mean-all for all), --since, --limit, --dry-run.
                        Wires its dependencies inline (does NOT reuse internal/wire).
internal/index          SHARED CONTRACT: Document, Chunk, Chunker, Embedder, Searcher, constants. Do not add deps here.
internal/chunk/markdown heading-aware Markdown chunker (implements index.Chunker)
internal/chunk/jira     Jira issue -> chunks (implements index.Chunker)
internal/embed/ollama   Ollama embedder (implements index.Embedder)
internal/search/postgres FTS searcher over ent (implements index.Searcher)
internal/search/qdrant  Qdrant client + searcher (implements index.Searcher)
                        Also implements pipeline.VectorStore (Upsert + Delete by point ID).
internal/retrieval      merges + reranks Postgres+Qdrant results, authority/boost rules
internal/ingest/gitlab  local-filesystem multi-root walk -> Document
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
internal/wire           shared wiring for cmd/scpbot, cmd/scpmcp, and cmd/scpingest (Services + Components)
internal/oas            ogen generated code
api/openapi.yaml        OpenAPI spec (source for ogen)
deploy/                 docker-compose + configs + .env.example
```

Service routing is currently inert: retrieval's `service` boost falls back to
1.0 when `metadata.service` is absent. Add real service routing only when query
quality demands it.

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
- Errors: wrap with `github.com/go-faster/errors` (`errors.Wrap`). No `fmt.Errorf("...%w")`.
- `errors.Wrap(f(), "msg")` as a return statement is wrong: if `f()` returns nil, `errors.Wrap` still returns a non-nil error. Always check first: `if err := f(); err != nil { return errors.Wrap(err, "msg") }`.
- File structure: split logical sections into separate files instead of separating them with `//` comments using `--` dividers. Even if a file seems large, prefer multiple focused files over in-file section markers.
- Logging: `*zap.Logger` passed in; no global loggers, no `log` package.
- Content hashing: `internal/index.Hash` (sha256 of normalized text). Skip re-embedding when hash is unchanged.
- IDs: `github.com/google/uuid`.

## Codegen

- ent: `go generate ./internal/ent/...` (runs `ent generate`).
- ogen: `go generate ./internal/oas/...`.
- Commit generated code in a **separate commit** from the schema/spec that produced it.

## Build / test

- Format: `golangci-lint fmt ./...` (do not hand-format).
- Lint: `golangci-lint run --fix ./...` (`--fix` can automatically fix some issues).
- Test: `make test` (or `make test_fast` = `go test ./...`).
- Tests must be hermetic, fast (no real sleeps), non-flaky, cross-platform. DB-backed tests use testcontainers or are skipped when no DB is available — convention: skip when `SCPBOT_TEST_DB` (postgres DSN) is unset.

## Ingestion

`make ingest` (= `go run ./cmd/scpingest all`) runs incremental backfills for every
configured source. Per-source: `make ingest-gitlab`, `make ingest-jira`,
`make ingest-telegram`.

Each source has a `SyncState` row in ent: `source`, `last_synced_at`,
`last_cursor` (opaque JSON), `status`, `error`, `document_count`. The CLI reads
the cursor before the run and writes it back per batch (jira) or per chat
(telegram) so a partial run resumes. GitLab has no cursor — it re-walks and
relies on pipeline body-hash skip.

`--reset <src|all>` wipes the source end-to-end: in one ent Tx it deletes
`documents`, `chunks`, and `SyncState` for that source (chunk IDs are captured
pre-delete), commits, then `qdrant.Delete` removes the freed point IDs. `--reset
all` refuses without `--yes-i-mean-all`.

`--since <RFC3339>` overrides the Jira cursor's `LastUpdated`. `--limit <int>`
caps docs per source. `--dry-run` fetches and logs counts without indexing.

## Run

`docker compose -f deploy/docker-compose.yml up` starts postgres + qdrant + ollama + the app.
Config via env (see `deploy/.env.example`).
