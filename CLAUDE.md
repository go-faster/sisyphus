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
internal/index          SHARED CONTRACT: Document, Chunk, Chunker, Embedder, Searcher, constants. Do not add deps here.
internal/catalog        service_catalog.yaml loader + service detection
internal/chunk/markdown heading-aware Markdown chunker (implements index.Chunker)
internal/chunk/jira     Jira issue -> chunks (implements index.Chunker)
internal/embed/ollama   Ollama embedder (implements index.Embedder)
internal/search/postgres FTS searcher over ent (implements index.Searcher)
internal/search/qdrant  Qdrant client + searcher (implements index.Searcher)
internal/retrieval      merges + reranks Postgres+Qdrant results, authority/boost rules
internal/ingest/gitlab  GitLab docs fetch -> Document
internal/ingest/jira    Jira issues fetch -> Document
internal/ingest/telegram gotd user-session backfill -> telegram_messages -> support_requests
internal/bot            gotd bot, /context handler
internal/ent            ent schema + generated code
internal/oas            ogen generated code
api/openapi.yaml        OpenAPI spec (source for ogen)
deploy/                 docker-compose + configs
service_catalog.yaml    manual service catalog (see plan §8)
```

## Conventions

- `internal/index` is the contract. It must stay dependency-light (stdlib + `github.com/google/uuid` only). All other packages depend on it, not on each other where avoidable.
- Implement the interfaces in `internal/index` exactly; do not change their signatures without updating this file and every implementer.
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
- Tests must be hermetic, fast (no real sleeps), non-flaky, cross-platform. DB-backed tests use testcontainers or are skipped when no DB is available.

## Run

`docker compose -f deploy/docker-compose.yml up` starts postgres + qdrant + ollama + the app.
Config via env (see `deploy/.env.example`).
