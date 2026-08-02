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

**`ARCHITECTURE.md` describes the target, not the code.** It lays out a redesign of today's
five role-based binaries into bounded contexts wired by an event spine, and it is **only
partly implemented** — sections marked _(today)_ are real, the rest is not built yet. Read
it for direction; do not treat it as a description of what exists, and do not "fix" code to
match it. **This file and the nested ones are the source of truth for how the system
actually works today.** When a piece of that migration lands, update the Layout below in
the same change.

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
- `cmd/ssingest` **†** — ingestion CLI (`git|files|gitlab|jira|telegram|all|index`), the `serve` daemon, the `worker` drain loop, plus `gc`/`repair`. The only webhook/poll owner.

**The contract**

- `internal/index` **†** — `Document`, `Chunk`, `Chunker`, `Embedder`, `Searcher`, `Answerer`, `Link`, constants. Everything depends on this; it depends on almost nothing.

**Ingestion**

- `internal/ingest` **†** (`git`, `gitlab`, `jira`, `telegram`, `alertmanager`) — per-source fetchers and their cursors. GitLab/Jira are also the event-spine source adapters: one fetch yields both an `index.Document` and a canonical `event.Event` (`EventFromMergeRequest`, `EventFromIssue`). `alertmanager` is push-only: it parses a webhook body into alert events and produces no documents.
- `internal/ingestrun` — shared GitLab/Jira incremental-run logic (indexes documents and routes their events via `Runner.Router`) + `IndexBatch`/`ResetSource`/`UpsertSyncState`, and `WithSourceLock`.
- `internal/webhook` — debounced `Trigger`, ticker `Poller`, GitLab/Jira webhook handlers (wake a fetcher) and the Alertmanager handler (routes the body itself, bearer-token auth). Used only by `ssingest serve`.
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
- `internal/agent` **†** — shared LLM tool-calling loop (`coreLoop`) behind both `/investigate` and agentic `/context`; `EventFromReport` publishes a finished investigation back onto the spine.
- `internal/agentclient` — HTTP client (submit + poll) ssbot uses against ssagent.
- `internal/agentstore` **†** — ent-backed `InvestigationJob` rows + dispatch through `internal/queue`; its `Subscriber` is the agent's `event.Handler`, turning a firing alert into a queued investigation.
- `internal/answer` **†** — agentic `/context` answerer; `search_knowledge`/`fetch_url` plus optional ssh-mcp sandbox.
- `internal/llm/openrouter` — non-agentic `/context` answerer.
- `internal/mcpserver` — MCP tool impls (search/answer/file/fetch) + `BearerAuthMiddleware`.
- `internal/mcpclient` — MCP client used to call tools exposed by ssmcp.
- `internal/content` — `index.ContentResolver`: `DatabaseReader`, `LocalRepoReader` (traversal-guarded), `ChainResolver`.
- `internal/fetch` — `index.URLFetcher` with a per-site allowlist (globs, methods, credentials, byte cap).
- `internal/notify` (+ `gitlab`, `jira`, `investigation`, `store`) — notifications: `event.Router` → projector → dispatcher/broadcaster → outbox → sink. Two addressing modes: `Dispatcher` matches an event's recipient to subscribed users (GitLab MR assignment, comments and merges, Jira issue assignment and comments); `Broadcaster` writes one row per chat registered with `/alerts on`, for events addressed to nobody in particular (`investigation` — an agent report on a firing alert). It fetches nothing; the GitLab/Jira source adapters emit the events (see `internal/ingest`). A source event states **current** membership, not a change to it, so `notify.Staleness` (`notify.max_assignment_age_seconds`, 24h by default) drops assignments the source dates older than the cutoff — otherwise any edit to a long-assigned issue re-announces its assignment, and a fresh outbox announces every one at once. It is deliberately permissive: an unknown timestamp still notifies, because over-notifying costs one message the dedup key collapses anyway while under-notifying loses a real assignment silently. The same cutoff bounds comment events (see below) and `mr_merged`, whose dedup key needs no timestamp — an MR merges once — but whose event says "merged" on every poll thereafter, so only `merged_at` tells a merge that just landed from one that landed months ago. **Who a GitLab/Jira actor is on Telegram comes from `notify.identities` in config and nowhere else** — ssapi reconciles it on startup (`store.SyncIdentities`), and there is no bot command to claim an identity, because nothing a user types proves the account is theirs. Subscriptions stay self-service. Contract and rationale are in `notify.go`'s package doc; delivery rides `internal/queue`.

**Infrastructure**

- `internal/event` — canonical `Event` (routable envelope + opaque `Payload`) and `Router`/`Handler`/`Subscription`, with an in-process `Mux`. Handlers must be idempotent on `Event.ID`.
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

**Notifications carry buttons the same way**, over their own rail: `notify.Button` →
outbox payload → `PendingNotification.buttons` → `bot.SendTo`, which sends text and
keyboard in one `sendMessage` (two messages would double the notification, and only the
first would carry the deduplicating `random_id`). The same guarantee holds and the
projector is where it is enforced — `alert` promotes only *annotation* values (a rule
author wrote those; an alert's labels and description carry whatever the alerting target
reported), GitLab/Jira promote only the subject's own URL, and `investigation` re-uses
`Report.Links`, already normalized. ssbot cannot build buttons itself: it has no DB and no
event, only the rendered row it drained over HTTP.

## Comments and mentions

Assignment says work arrived; comments say the conversation about it moved. Both ride the
same events (`mr.updated`, `issue.updated`) — the payload carries the object's newest
comments alongside its current members, and `notify.CommentRule` (`internal/notify/comments.go`)
holds the fan-out rule for both sources, because that rule is where the failure modes are:

- **Volume is asymmetric, so the two events fan out differently.** Assignment is one
  message per object ever; comments are unbounded. A **mention** notifies per comment —
  being named is explicit and rare, and collapsing two would lose a question addressed to
  you. A **comment** notifies once per (object, recipient) per batch, for the newest one
  they did not write. A thread that gets twenty replies between two polls is one message.
- **The dedup id is the comment id**, never a timestamp: an edit keeps the id, so editing
  a comment does not re-notify. Keying the coalesced comment event by the *newest*
  comment's id is what keeps the next poll's newest comment a fresh notification rather
  than a permanently suppressed one.
- **Mentions are parsed by the source adapter, not the projector** — only it knows that
  GitLab writes `@username` and Jira writes `[~accountid:…]`/`[~username]`, and the
  extracted keys land in the same id space the recipient is matched on.
- **`notify.Staleness` is the backfill guard.** The poll is incremental on `updated_after`
  and the event states current comments, so without a cutoff the first run after this
  shipped would announce every comment in the fetched window. Comments are *not* subject
  to the assignment's staleness result, though: a comment on an MR assigned to you months
  ago is still news.
- A comment's button opens the comment (`#note_<id>` on GitLab, `?focusedCommentId=` on
  Jira) — a fragment/parameter on the URL the API returned, never a guess at a permalink.

Out of scope, deliberately: watchers/participants/subscribers (needs fetching that does
not happen), and GitLab *issues*, which emit no canonical event at all today.

## Alerts: fire → investigate → announce

One loop spanning four packages, so the rule lives here.

`POST /webhooks/alertmanager` (on `ssingest serve`) decodes an Alertmanager group into
`event.TypeAlertFiring` events. With `alertmanager.investigate.enabled`, `agentstore.Subscriber`
submits each as an investigation keyed by `Event.ID`. A worker in `ssagent` runs it, persists the
report, then routes `event.TypeInvestigationCompleted` back onto the spine, where
`notify.Broadcaster` writes one outbox row per **registered chat** for ssbot to deliver.

**Every hop dedups on an id, and no hop is per-user.** The alert's id pins the
firing/resolved transition (not the delivery, so a `repeat_interval` resend is the same
occurrence); the investigation's id is the job's (not its finish time); the outbox key is
per (chat, event). An alert is addressed to whoever watches the channel, not to a linked GitLab/Jira identity,
which an alert does not have. That is why `Notification.user_id` is nullable and
`peer_type` exists: a broadcast row is addressed by its Telegram peer alone.

**Chats register themselves, from inside the chat.** `/alerts on` in a channel or group
writes a `notify_chats` row (`internal/notify/store.RegisterChat`), and the peer it stores
comes from the update that carried the command. That is not a convenience: over MTProto an
`InputPeerChannel` needs an access hash, a private channel has no username to resolve one
from later, and a bare `-100…` id addresses nothing on its own. The update is the only
place that hash exists, so capturing it there is what makes a private channel a valid
target at all. Re-running `/alerts on` re-stores the hash, which is how a rotated bot
session heals; `/alerts off` keeps the row so the hash survives. The bot must be an admin
in a channel to receive the command there in the first place.

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
- Payload versioning: anything JSON-encoded for a *reader in another process* declares the shape it was written in — `event.Event.PayloadVersion` (stamped by `WithPayload`, demanded by `DecodePayload`) for canonical events, `indexjob.Version` for queued index jobs. A reader that meets a version it does not know fails by name (`event.PayloadVersionError`, `indexjob.ErrVersion`) rather than decoding an old shape into today's structs. Bump on a field's type or meaning changing; additive fields need no bump. Do not add a lenient decoder to paper over a shape change — the version is how the two sides disagree loudly.
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
