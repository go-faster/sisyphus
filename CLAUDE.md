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

## Nested CLAUDE.md

Directories marked **†** below carry their own `CLAUDE.md` with the invariants and
past mistakes specific to that package. Read it before changing anything there —
that is where the "do not undo this" reasoning lives, deliberately kept out of this
file so it costs nothing until you actually open the directory.

The corollary: anything needed to decide *which* directory to open has to stay here.
Keep the index below one line per package, and put the depth in the nested file.

## Layout

**Entry points**

- `cmd/ssapi` — HTTP API (bearer auth on `/search`, `/context`; `/health` public). Stateless, safe at N replicas. Its `migrate` subcommand (`wire.Migrate`) is the **only** place schema migrations run.
- `cmd/ssbot` — Telegram bot; reaches ssapi via `internal/apiclient`. Single replica (two bots double-answer).
- `cmd/ssagent` — `/investigate` HTTP service. Persists job + queue row in one tx, returns 202; any replica's worker runs it. Never migrates.
- `cmd/ssmcp` — MCP server (Streamable HTTP or stdio); calls ssapi via `internal/apiclient`.
- `cmd/ssingest` **†** — ingestion CLI (`git|files|gitlab|jira|telegram|all|index|notify`), the `serve` daemon, the `worker` drain loop, plus `gc`/`repair`. The only webhook/poll owner.

**The contract**

- `internal/index` **†** — `Document`, `Chunk`, `Chunker`, `Embedder`, `Searcher`, `Answerer`, `Link`, constants. Everything depends on this; it depends on almost nothing.

**Ingestion**

- `internal/ingest` **†** (`git`, `gitlab`, `jira`, `telegram`) — per-source fetchers and their cursors.
- `internal/ingestrun` — shared GitLab/Jira incremental-run logic + `IndexBatch`/`ResetSource`/`UpsertSyncState`, and `WithSourceLock`.
- `internal/webhook` — debounced `Trigger`, ticker `Poller`, GitLab/Jira webhook handlers. Used only by `ssingest serve`.
- `internal/indexjob` **†** — the boundary between ingestion's two halves, over the `ingest.index` queue.
- `internal/pipeline` **†** — `Pipeline.Index`: idempotent doc+chunk upsert, embed, vector upsert/delete. `Skipper` answers the skip question without doing the work.
- `internal/vectorgc` **†** — `ssingest gc`: drop vector points no chunk references.
- `internal/vectorrepair` **†** — `ssingest repair`: re-embed chunks whose point is keyed by the wrong ID.

**Chunking, embedding, search**

- `internal/chunk/{markdown,git,gitlab,jira}` — `index.Chunker` implementations.
- `internal/embed/ollama` — `index.Embedder`.
- `internal/search/postgres` — FTS searcher over ent.
- `internal/search/qdrant` **†** — Qdrant client + searcher; also `pipeline.VectorStore`.
- `internal/retrieval` — merges Postgres+Qdrant via RRF (k=60), then authority/boost rules.

**Answering**

- `internal/api` — the generated ogen `Handler`; bridges HTTP to retrieval + answerer.
- `internal/apiclient` — `oas.Client` adapter satisfying bot/mcpserver's `Retriever` + `index.Answerer` over HTTP.
- `internal/bot` — gotd bot, `/context` handler, `linksMarkup`.
- `internal/agent` **†** — shared LLM tool-calling loop (`coreLoop`) behind both `/investigate` and agentic `/context`.
- `internal/agentclient` — HTTP client (submit + poll) ssbot uses against ssagent.
- `internal/agentstore` **†** — ent-backed `InvestigationJob` rows + dispatch through `internal/queue`.
- `internal/answer` **†** — agentic `/context` answerer; `search_knowledge`/`fetch_url` plus optional ssh-mcp sandbox.
- `internal/llm/openrouter` — non-agentic `/context` answerer.
- `internal/mcpserver` — MCP tool impls (search/answer/file/fetch) + `BearerAuthMiddleware`.
- `internal/mcpclient` — MCP client used to call tools exposed by ssmcp.
- `internal/content` — `index.ContentResolver`: `DatabaseReader`, `LocalRepoReader` (traversal-guarded), `ChainResolver`.
- `internal/fetch` — `index.URLFetcher` with a per-site allowlist (globs, methods, credentials, byte cap).
- `internal/notify` (+ `gitlab`, `jira`, `store`) — per-user GitLab MR-assignment / Jira issue-assignment notifications: collector → dispatcher → outbox → sink. Contract and rationale are in `notify.go`'s package doc; delivery rides `internal/queue`.

**Infrastructure**

- `internal/queue` **†** — shared background-work substrate (one `queue_jobs` table, `queue` column). Notify delivery, agent investigations, ingest indexing.
- `internal/ent` — ent schema + generated code. `internal/ent/migrate` **†** holds the versioned SQL and the `Runner`.
- `internal/config` **†** — YAML + env config loading.
- `internal/wire` — shared wiring for ssapi/ssingest (`Services` + `Components`).
- `internal/httpmw` — small net/http middlewares shared by ssapi/ssmcp.
- `internal/netclient` — builds outbound HTTP clients (proxy, retry, metrics) from config.
- `internal/telemetry` — OpenTelemetry helpers.
- `internal/oas` — ogen generated code. Do not edit.
- `internal/indextest` — reusable mocks for `index` interfaces.
- `internal/smoke` — `//go:build integration` cross-source ingest+search smoke test (`make test_integration`).
- `internal/cliversion`, `internal/cmdutil` — binary version plumbing.

**Non-Go**

- `api/openapi.yaml` — source for ogen.
- `deploy/` — docker-compose + configs + `.env.example`.
- `deploy/helm/sisyphus` — the whole stack on Kubernetes; `values.config` **is** config.yaml. See `deploy/helm/README.md`.

Service routing is currently inert: retrieval's `service` boost falls back to 1.0 when
`metadata.service` is absent. Add real service routing only when query quality demands it.

## Answers & link buttons

Answers can carry actionable links rendered as Telegram inline URL buttons. This spans
five packages, so the rule lives here rather than in any one of them.

`index.Link{Text,URL}` (`Valid()` requires an absolute http(s) URL + non-empty label) and
`index.Answer{Text,Links}` are the shared types. Buttons cross HTTP as
`ContextResponse.buttons`; `internal/api` populates, `internal/apiclient` re-validates,
`internal/bot` renders.

**The guarantee: a button URL must come from a vetted source, never from content.** Both
`/context` paths constrain `submit_answer`'s buttons to the retrieved sources' `source_url`
(`filterButtons`), so the model cannot surface a hallucinated or off-context link. The
agentic path additionally allows URLs the loop *discovers* mid-conversation — and
`agent.collectURLs` extracts those **only** from structured `"source_url"`/`"url"` JSON
keys in a tool result, never by regexing the result text. Tool results carry untrusted
ingested content (a chunk's body, a fetched page); a whole-text URL scan would promote any
link merely *mentioned* there into a clickable button. Keep this restriction if
`collectURLs` or its call site changes.

`/investigate` is deliberately looser: `Report.Links` may be any http(s) URL the agent got
from tool results (dashboards, tickets). `Report.normalize` drops invalid/duplicate links
and caps at `maxReportLinks`.

## API auth

`cmd/ssapi` requires a shared static bearer token (`api.auth_token` /
`SISYPHUS_API_AUTH_TOKEN`), enforced by `internal/api.SecurityHandler` and attached by
`internal/apiclient`. `/health` is the only unauthenticated route.

`cmd/ssmcp`'s `/mcp` has *optional* bearer auth (`mcp.auth_token` /
`SISYPHUS_MCP_AUTH_TOKEN`, `internal/mcpserver.BearerAuthMiddleware`). Unlike ssapi, an
empty token does **not** fail startup — it logs a warning and serves `/mcp`
unauthenticated. Set it in any deployment reachable from untrusted networks.

## Conventions

- `internal/index` is the contract. Implement its interfaces exactly; do not change a signature without updating every implementer and the relevant CLAUDE.md.
- Configuration: struct-based options with `setDefaults()`, not functional options (`Option func(*T)`):
  ```go
  type FooOptions struct {
      Logger  *zap.Logger
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
- Configuration lists are YAML sequences of objects, not comma-separated strings: `gitlab.projects: [{ref: group/docs}]`, `jira.projects: [{key: ABC}]`, `telegram.monitor_chats: [{id: -100123, username: support}]`.
- Errors: wrap with `github.com/go-faster/errors` (`errors.Wrap`). No `fmt.Errorf("...%w")`.
- Logging: `*zap.Logger` passed in; no global loggers, no `log` package.
- IDs: `github.com/google/uuid`.
- Content hashing: `internal/index.Hash` (sha256 of normalized text). Skip re-embedding when the hash is unchanged.
- Document identity: unique on `(source, source_id)` — **not** `body_hash`. Re-ingesting the same `source_id` with changed content updates the row and its chunks in place; it never creates a duplicate.
- Changing a chunker's output for input it already handled (different boundaries, text, or chunk types)? Bump `index.VersionedChunker.ChunkerVersion()`. The body hash cannot see a chunker change — same body, different code — so without a bump, already-indexed documents keep chunks built by the old code until someone runs a full `--reset`. A bump re-chunks only that chunker's documents, and chunks whose text is unchanged still reuse their embeddings, so it is cheap. A chunker declaring no version reports 0 and is never re-chunked.

## Codegen

- ent: `go generate ./internal/ent/...`. Renaming a schema type: delete the old generated files first (`internal/ent/<oldname>*.go`, `internal/ent/<oldname>/`) — generate errors trying to open the removed schema file otherwise.
- ogen: `go generate ./internal/oas/...`. Both: `make codegen`.
- Commit generated code in a **separate commit** from the schema/spec that produced it.
- After changing `internal/ent/schema`, generate the migration: `make migrate-diff NAME=add_foo_column` (needs a local Docker daemon, nothing else running). Hand-written or data migrations need `make migrate-hash` afterwards. See `internal/ent/migrate/CLAUDE.md` before touching anything in `migrations/`.

## Build / test

- Run `go generate`, `go build`/`vet`/`test`, and `golangci-lint` **outside the sandbox** — the module/build cache isn't sandbox-writable and these fail with a read-only-filesystem error otherwise.
- Format: `golangci-lint fmt ./...` (do not hand-format).
- Lint: `golangci-lint run --fix ./...`. `dogsled` flags 3+ blank identifiers in any statement, not just `:=` declarations — discard extra return values with separate `_ = x` statements, not `_, _, _ = a, b, c`.
- Test: `make test` (or `make test_fast` = `go test ./...`; `make test_integration` adds `-tags integration`, which needs Docker).
- Tests must be hermetic, fast (no real sleeps), non-flaky, cross-platform. DB-backed tests skip when `SISYPHUS_TEST_DB` (postgres DSN) is unset. **That name is the only gate** — a suite gated on anything else runs nowhere.
- CI's `test-db` job (`.github/workflows/x.yml`) sets `SISYPHUS_TEST_DB` against a Postgres service and runs the whole suite with `-p 1`. The shared `test` job cannot: its matrix spans macOS/Windows and the reusable workflow takes no service container.
- The DB-backed suites share one database, so a suite must delete only its own fixtures (scope by source prefix or table) — wiping a table deletes another package's rows mid-test. Packages also run concurrently, so literal fixture values (usernames, keys) can collide on a shared unique column; use suite-distinct values, not just cleanup scoping.
- Locally: `docker run --rm -e POSTGRES_PASSWORD=test -e POSTGRES_USER=test -e POSTGRES_DB=test -p 5433:5432 postgres:17-alpine`, then `SISYPHUS_TEST_DB="postgres://test:test@127.0.0.1:5433/test?sslmode=disable" go test ./...`.

## Ingestion

`make ingest` (= `go run ./cmd/ssingest all`) runs incremental backfills for every
configured source, once, then exits. Per-source: `make ingest-git`, `make ingest-gitlab`,
`make ingest-jira`, `make ingest-telegram`. `make ingest-serve` runs the daemon instead.

Ingestion has **two halves with opposite scaling properties**, split across the
`ingest.index` queue: fetch (`ssingest serve`) is single-owner because it holds
credentials and advances cursors; index (`ssingest worker`) is stateless and scales with
replicas. `cmd/ssingest/CLAUDE.md` has the full topology, the subcommand flags
(`--reset`/`--since`/`--limit`/`--dry-run`), and the locking rules — read it before
changing how a run is scheduled or how documents reach the queue.

## Run

`docker compose -f deploy/docker-compose.yml up` starts postgres + qdrant + ollama + the
app. Config via env (see `deploy/.env.example`).

Kubernetes: `helm upgrade --install sisyphus deploy/helm/sisyphus -f my-values.yaml`. The
chart mirrors the compose stack and encodes three invariants: `ssingest`/`ssbot` are
single-replica with a `Recreate` strategy (two schedulers race on source rows, two bots
double-answer); `ssingestWorker` is the opposite — replicas>1, RollingUpdate, no PVC, and
enabling it flips `config.ingest.worker.enabled` to false so the scheduler stops indexing
in-process; and the sandbox is egress-denied by NetworkPolicy with ingress only from its
MCP front-end. Adding an MCP upstream is a values-only change under `mcp.servers` —
Deployment, Service and the `gateway.toml` `[[upstream]]` all generate from one entry.
