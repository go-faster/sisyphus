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
cmd/ssapi              owns Postgres/ent; serves the HTTP API (bearer-token auth on
                        /search, /context; /health public). Stateless, safe to run N
                        replicas. Its `migrate` subcommand (wire.Migrate) is the only
                        place schema migrations run, as a one-shot pre-install/
                        pre-upgrade hook Job (Helm) or compose service — never a
                        serving replica.
cmd/ssbot              Exposes system as Telegram bot; talks to ssapi via internal/apiclient
cmd/ssagent            HTTP service for /investigate over agent.Investigator's LLM
                        tool-calling loop. POST /investigate persists a job row
                        (internal/agentstore) plus a queue job (internal/queue) in one
                        tx and returns 202 + job ID immediately; a queue.Worker in the
                        same process — or in any other replica — claims and runs it, so
                        ssagent is a queue worker and safe to scale to N replicas.
                        GET /investigate/{id} polls for the result. Connects to Postgres
                        (database.dsn) but never migrates (only ssapi does).
                        internal/agentclient is the HTTP client (submit + poll) ssbot
                        uses to talk to it.
cmd/ssmcp              MCP server entrypoint (Streamable HTTP or stdio); calls ssapi via internal/apiclient
cmd/ssingest           ingestion CLI + daemon: git|files|gitlab|jira|telegram|all subcommands
                        run one-shot (--reset <src|all> (--yes-i-mean-all for all), --since,
                        --limit, --dry-run); `serve` instead runs as a long-lived daemon that
                        triggers the same per-source runs on GitLab/Jira webhooks (internal/webhook
                        handlers) and on a poller (git/files/gitlab/jira/telegram each on their
                        own interval), so no external cron is needed. ssingest is the only
                        webhook/poll owner — ssapi does not run ingestion. Wires its dependencies
                        inline (does NOT reuse internal/wire, only internal/wire.NewServices).
                        `worker` is the other half: it drains the `ingest.index` queue and does
                        nothing else. See "Ingestion topology" below — `serve` fetches and
                        publishes, `worker` indexes, and `worker` is the one that scales.
                        `gc` is the odd one out: not ingestion, but a one-shot sweep of
                        vector-store points no chunk references (internal/vectorgc).
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
internal/ingestrun      shared GitLab/Jira incremental-run logic (Runner.RunGitLab/RunJira) used
                        by both cmd/ssingest's one-shot subcommands and its `serve` daemon mode;
                        also IndexBatch/ResetSource/UpsertSyncState helpers shared with git/files/
                        telegram runs, which live directly in cmd/ssingest (package main) since
                        they're not needed anywhere else.
internal/webhook        debounced Trigger (coalesces a webhook + a poll tick racing on the same
                        source into one run) + ticker-based Poller (fire-on-start, then every
                        interval) + GitLab/Jira webhook http.Handlers (X-Gitlab-Token/X-Jira-Token
                        validation). Used only by cmd/ssingest's `serve` subcommand.
internal/pipeline       Pipeline.Index: idempotent doc+chunk upsert (ent) + embed (Ollama)
                        + vector Upsert/Delete (Qdrant). Per-chunk embedding skip (preserves
                        unchanged chunks' qdrant_point_id) and stale-point cleanup on changed docs.
                        pipeline.Skipper answers Index's skip question without doing the work,
                        for a producer filtering documents before they cost a queue row; it
                        shares `unchanged` with Index rather than reimplementing it, because a
                        producer that under-reports change silently stops indexing.
                        The document-level skip (skip.go's `unchanged`) must cover EVERY input
                        that shapes the output: body hash, doc.URL (propagated onto chunks as
                        source_url), and the chunker version. Anything left out is a field that
                        can change while indexing says "unchanged" forever — a document's body is
                        the only thing that normally moves, so nothing else ever forces a revisit.
                        VectorStore interface: Upsert + Delete. The stale-point delete runs
                        AFTER the ent tx commits, so it cannot be rolled back: it retries
                        (deleteStaleVectors), and on final failure leaks orphaned points that
                        only `ssingest gc` can reclaim. Non-fatal by design — the document is
                        indexed correctly either way.
                        INVARIANT: a chunk's vector point is keyed by the chunk's own ID
                        (chunks.id == chunks.qdrant_point_id). Retrieval hydrates a vector hit's
                        text from Postgres BY CHUNK ID, so a point under any other ID resolves to
                        empty text forever. Index enforces this by adopting the existing row's ID
                        when a chunk matches (index, text_hash) — persist's upsert keeps the row's
                        ID on conflict, so embedding under the chunker's fresh UUID would break it.
                        The stale cleanup deletes the point ID the row RECORDED (not the row's own
                        ID, which misses the real point on rows that drifted before this was
                        enforced) and drops stale rows whether or not they were ever embedded
                        (never-embedded leftovers stay visible to Postgres FTS otherwise).
                        internal/vectorrepair repairs rows that already drifted.
internal/vectorgc       `ssingest gc`: deletes vector points no chunk references (Postgres is
                        the source of truth). Two passes separated by Grace: Index upserts
                        points BEFORE committing chunk rows, so a doc mid-index looks exactly
                        like an orphan, and deleting one is unrecoverable (the row lands with
                        qdrant_point_id set, so it is never re-embedded and vector search can
                        never reach it). Never collapse this into a single pass.
internal/vectorrepair   `ssingest repair`: re-embeds chunks whose point is keyed by the wrong
                        ID (chunks.id != qdrant_point_id) so vector hits can hydrate again.
                        Writes the new point BEFORE rebinding the row and deletes the old one
                        after, so an interrupted run leaves an orphan (gc's job), never a row
                        pointing at nothing.
internal/indexjob       the boundary between ingestion's two halves: Kind (which chunker),
                        Payload (one document per job), Publisher (producer side) and
                        Handler (worker side), over the `ingest.index` queue.
                        A job carries the DOCUMENT, not a reference to it — that is what
                        lets a worker run with no source credentials, clone or session file.
                        TWO THINGS ARE LOAD-BEARING:
                        (1) Decode REHYDRATES metadata. The GitLab and Jira ingesters put a
                        concrete Go struct in index.Document.Metadata (map[string]any) and
                        their chunkers recover it by type assertion. After a JSON round-trip
                        it is a map[string]any, the assertion fails, and the chunker falls
                        through to its untyped fallback: one flat chunk instead of typed
                        summary/comment chunks, with NO error and NO log. Every issue and MR
                        would index worse forever and only --reset would reach them. A new
                        struct-valued metadata key needs a case in rehydrate; TestRoundTripChunks
                        is the only thing that catches one that was not.
                        (2) Canonicalize puts the INLINE path through the same JSON
                        normalization, because a metadata value's Go type is observable:
                        internal/search/qdrant maps an int to a Qdrant integer and a float64
                        to a double, and DROPS a []string entirely (qdrant.NewValue has no
                        case for it, and addPayloadValue swallows the error) while a []any of
                        the same strings converts fine. Without it, `ssingest gitlab` and
                        `ssingest serve` would write different Qdrant payloads for the same
                        document. Inert for retrieval today — every condition is a keyword
                        match — but do not let the two paths diverge again.
                        Publisher does NOT dedup by content. Keying on
                        (source, source_id, body_hash) looks right and is wrong: queue dedup
                        covers a job's whole lifetime, so a document edited A->B and reverted
                        to A finds its key spent and is never re-indexed. It publishes under a
                        fresh key and relies on pipeline.Index being idempotent instead.
internal/queue          SHARED SUBSTRATE for background work (notify delivery, agent
                        investigations, and ingest as it moves over). One queue_jobs
                        table serves every queue, distinguished by the `queue` column,
                        so there is one claim path and one set of indexes.
                        Postgres.Fetch claims with FOR UPDATE SKIP LOCKED, N workers
                        drain concurrently without coordination, and Nack retries with
                        backoff until MaxAttempts, after which the job is terminal
                        (status=error) and stays for inspection.
                        THREE THINGS ARE LOAD-BEARING and were each a measured or
                        prior-art-confirmed mistake before being fixed — do not undo:
                        (1) `visible_at` is ONE column serving both "claimable at" and
                        "claim expires at". Split into available_at/lease_expires_at,
                        the OR between them defeats index ordering and Postgres sorts
                        every matching row before LIMIT: measured 76ms and a 9.4MB
                        external merge sort per claim on a 200k backlog, versus 0.06ms
                        after. The partial index (queue, visible_at) WHERE status IN
                        ('pending','running') is what serves both the filter and the
                        ORDER BY, and it holds only outstanding work — terminal jobs
                        leave the index, so history costs claims nothing (1.3MB vs the
                        34MB full index on the same table).
                        (2) Time comes from POSTGRES, not from Go. Queries say
                        COALESCE($n, now()) and PostgresOptions.Now is nil outside
                        tests. Per-process clocks mean a replica running fast sees live
                        claims as expired and steals them. (pgmq uses clock_timestamp(),
                        dataddo/pgq CURRENT_TIMESTAMP — nobody sane uses client time.)
                        (3) A handler's deadline is the CLAIM's deadline
                        (Delivery.Deadline), not a separate configured timeout. There is
                        deliberately no WorkerOptions.JobTimeout: two independent knobs
                        drift, and a handler outliving its claim means two workers run
                        the same job.
                        queue.Worker is the drain loop (claim -> run -> ack/nack); it
                        claims only as many jobs as it has free slots, so a backlog
                        never sits claimed behind a busy handler.
                        The interface carries PAYLOADS and acks by ID — it never hands
                        out rows, table names, or transactions — so a broker-backed
                        implementation stays possible. Two things follow, and must not
                        be designed away: dedup is best-effort (consumers must be
                        idempotent, since delivery is at-least-once), and transactional
                        enqueue is Postgres.WithTx's guarantee, NOT the interface's.
                        Job state of record (a report, a delivery outcome) belongs on
                        the domain row, never in the queue: a queue answers "what work
                        is outstanding", never "what happened to job X".
                        Producers that keep a domain row set Message.ID to that row's ID
                        so the two share an identifier. Set Message.Key to that ID too
                        unless the queue is genuinely the dedup point — a queue job
                        outlives the row it refers to, so reusing a business dedup key
                        silently swallows a re-enqueue after the old row is cleaned up.
                        NOT YET SOLVED: rows are never deleted and every job costs 2+
                        UPDATEs, so the table accumulates dead tuples with no retention,
                        archiving, partitioning or autovacuum tuning. Every comparable
                        system treats this as mandatory (pgq/pgq is append-only +
                        TRUNCATE rotation with autovacuum_enabled=off; pgmq deletes on
                        ack or archives; dataddo/pgq partitions via pg_partman). Fix
                        before this carries real volume.
internal/bot            gotd bot, /context handler
internal/ent            ent schema + generated code (Document, Chunk, SupportRequest,
                        TelegramMessage, SyncState, QueueJob)
internal/llm/openrouter OpenRouter-backed LLM answerer (chat completions) used to
                        answer /context questions from retrieved chunks (non-agentic path).
internal/agent          shared LLM tool-calling loop engine (coreLoop in core.go) used by
                        BOTH /investigate (loop.go, Report/submit_report terminal tool) and
                        the agentic /context path (internal/answer.ContextLoop); also collects
                        DiscoveredURLs from tool results (source_url/url JSON fields only —
                        never free-form body text, see "Answers & link buttons")
internal/agentstore     ent-backed persistence for cmd/ssagent's InvestigationJob rows
                        (pending/running/done/error, idempotency_key, Report snapshot),
                        plus dispatch through internal/queue (queue "agent.investigate").
                        Store.Submit is the dedup point: a repeated idempotency_key
                        returns the existing job instead of creating a second one, and
                        writes the row + its queue job in one tx so a submitted job is
                        never queued twice nor accepted without being queued.
                        Store.ReapStale settles ONLY jobs the queue abandoned (attempts
                        spent, no live lease). It must never sweep all pending/running
                        rows: with a shared queue, "running" means "running on some
                        replica", and a blanket sweep reports live investigations as
                        dead.
internal/answer         agentic /context answerer (index.Answerer): AgenticAnswerer runs
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
deploy/helm/sisyphus    Helm chart: the whole stack on Kubernetes (datastores, app,
                        mcpgateway + `mcp.servers` upstreams, sandbox, otelcol).
                        `values.config` IS config.yaml, merged over chart-computed
                        in-cluster endpoints. See deploy/helm/README.md.
```

Service routing is currently inert: retrieval's `service` boost falls back to
1.0 when `metadata.service` is absent. Add real service routing only when query
quality demands it.

## Answers & link buttons

Answers can carry actionable links rendered as Telegram inline URL buttons.

- `index.Link{Text,URL}` (`Valid()` requires an absolute http(s) URL + non-empty
  label) and `index.Answer{Text,Links}` are the shared types. `index.Answerer`
  accepts the full `index.Query` and always returns `index.Answer`, so query
  controls and link buttons are preserved through API, bot, and MCP paths.
- `/context` (`internal/llm/openrouter.Answerer.Answer`): the answerer prompt
  tells the model to cite sources as inline Markdown links, and a `submit_answer`
  tool returns `{answer, buttons}`. Button URLs are validated **and constrained to
  the retrieved sources' `source_url`** (see `filterButtons`) so the model can't
  surface a hallucinated/off-context URL. Buttons cross the HTTP boundary as
  `ContextResponse.buttons` (oas `Link`); `internal/api` populates them,
  `internal/apiclient.Answer` reads them (re-validating in `fromLinks`), and
  `internal/bot` renders them via `linksMarkup` on the `/context` reply.
- `/investigate` (`internal/agent`): `Report.Links` comes from the `submit_report`
  tool's `links` param; `Report.normalize` drops invalid/duplicate links and caps
  at `maxReportLinks`. Here links may be **any** http(s) URL the agent obtained from
  tool results (dashboards, tickets), not just cited sources. The bot attaches them
  to the final report message.
- Agentic `/context` (`internal/answer.AgenticAnswerer.Answer`, opt-in via
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
HTTP API to attach health checks to), `agent.addr` (ssagent), `ingest.addr`
(`ssingest serve`'s health/webhook server). The old flat
`http_addr`, `mcp_addr`, and `mcp_auth_token` top-level keys still parse for
backwards compatibility but are deprecated: using one logs a warning
(`Config.Warnings`, surfaced via `Config.LogWarnings`), and setting both the
old and new field for the same value is a hard error at `config.Load()` time.
See `internal/config/config.go`'s `resolveDeprecatedAddr`/`resolveDeprecatedSecret`.

`ingest.worker.*` (`IngestWorkerConfig`) controls the indexing half: `enabled`
(default **true** — a plain bool in YAML would make every config that omits the
section silently disable in-process indexing, so the file struct holds a `*bool`
and `resolve()` applies the default), `concurrency`, `lease_seconds`,
`max_attempts`, `poll_interval_seconds`. `lease_seconds` is also the handler's
deadline (`queue.Delivery.Deadline` — there is deliberately no second timeout
knob), so it must comfortably exceed the slowest single document's
embed-and-upsert or that document is reclaimed mid-run and retried forever.

`ingest.*` (`IngestConfig`) controls `ssingest serve`'s daemon polling for the sources
that have no config section of their own to hold a poll interval: `ingest.git.poll.interval_seconds`,
`ingest.files.poll.interval_seconds`, `ingest.telegram.poll.interval_seconds` (all 0 by
default — polling for that source is off unless set). GitLab and Jira reuse their own
existing `gitlab.poll.interval_seconds`/`jira.poll.interval_seconds` and
`gitlab.webhook.*`/`jira.webhook.*` — the same config keys, just now consumed by
`ssingest serve` instead of `ssapi` (see cmd/ssingest/cmd_serve.go).

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
- Changing a chunker's output for input it already handled (different boundaries, text, or
  chunk types)? Implement/bump `index.VersionedChunker.ChunkerVersion()` on it. The body hash
  cannot see a chunker change — same body, different code — so without a bump, documents
  already indexed keep chunks built by the old code until someone runs a full `--reset`.
  Bumping re-chunks only that chunker's documents; chunks whose text is unchanged still reuse
  their embeddings, so a bump is cheap. A chunker that declares no version reports 0 and is
  never re-chunked.
- IDs: `github.com/google/uuid`.
- Document identity: unique on `(source, source_id)` — not `body_hash`. Re-ingesting the
  same `source_id` with changed content updates the existing row and its chunks in place
  (see `internal/pipeline.Pipeline.Index`); it never creates a duplicate document.

## Codegen

- ent: `go generate ./internal/ent/...` (runs `ent generate`). Renaming a schema type: delete
  the old generated files first (`internal/ent/<oldname>*.go`, `internal/ent/<oldname>/`) —
  generate errors trying to open the removed schema file otherwise.
- ogen: `go generate ./internal/oas/...`.
- Commit generated code in a **separate commit** from the schema/spec that produced it.

### Schema migrations

`internal/ent/schema` is the single source of truth for the DB schema. Versioned SQL
migration files live in `internal/ent/migrate/migrations/` and are applied by the
hand-written `Runner` in `internal/ent/migrate/runner.go` (tracked via a
`schema_migrations` table). `Runner.Run` takes a Postgres advisory lock
(`internal/ent/migrate/runner.go`'s `advisoryLockID`) for its whole duration, so
concurrent callers serialize instead of racing a `schema_migrations` primary-key
conflict — the second caller blocks, then finds nothing pending once the first
commits.

Migrations run out of the serving path entirely, via the one-shot `ssapi migrate`
subcommand (`wire.Migrate`) — never a long-running replica. In Helm this is the
`migrateJob` pre-install/pre-upgrade hook Job (`deploy/helm/sisyphus/templates/migrate-job.yaml`),
which Helm blocks on before creating or updating any Deployment; in docker-compose
it's the `ssmigrate` one-shot service, which `ssapi`/`ssingest` depend on via
`condition: service_completed_successfully`. No serving process — `ssapi` (now safe
to run as N replicas), `ssingest`, `ssbot`, `ssmcp` — migrates itself or holds any
`RunMigrations`-style flag. `ssapi`'s own readiness check (`internal/wire.healthChecker`)
fails `/ready` while `Runner.Pending` reports any migration not yet applied, so a
serving replica never accepts traffic against a schema it doesn't know — this is also
why `ssingest`'s `wait-for-ssapi` init container transitively waits for the schema too.

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
still hand-written directly in `migrations/` — as are data migrations, which a schema
diff cannot produce at all (e.g. `20260723061500_backfill_notification_queue.sql`,
which gives already-queued notifications a delivery job so the outbox move doesn't
strand them).

A hand-written file invalidates `atlas.sum`, and every later `make migrate-diff` then
refuses to start on the checksum mismatch. Run `make migrate-hash` to rehash the
directory (no Docker needed), then `make migrate-diff` to confirm it replays cleanly
and produces no new file — no new file means the ent schema and the migrations agree.
Never hand-edit `atlas.sum`.

Migration files must not contain more than the forward migration: the runner execs the
entire file as one blob with no down/rollback support, and stray SQL after a `-- +goose
Down`-style comment will actually execute.

A migration not yet merged/deployed: squash it (delete the migration file(s), restore the
prior `atlas.sum`, rerun `make migrate-diff`) rather than layering a rename/follow-up
migration on top — avoids a permanently dangling table in every future environment's history.

## Build / test

- Run `go generate`, `go build`/`vet`/`test`, and `golangci-lint` outside the sandbox — the
  module/build cache isn't sandbox-writable and these fail with a read-only-filesystem error
  otherwise.
- Format: `golangci-lint fmt ./...` (do not hand-format).
- Lint: `golangci-lint run --fix ./...` (`--fix` can automatically fix some issues). `dogsled`
  flags 3+ blank identifiers in any statement, not just `:=` declarations — discard extra
  return values with separate `_ = x` statements, not `_, _, _ = a, b, c`.
- Test: `make test` (or `make test_fast` = `go test ./...`).
- Tests must be hermetic, fast (no real sleeps), non-flaky, cross-platform. DB-backed tests use testcontainers or are skipped when no DB is available — convention: skip when `SISYPHUS_TEST_DB` (postgres DSN) is unset. That name is the ONLY gate: a suite gated on anything else runs nowhere.
- CI's `test-db` job (`.github/workflows/x.yml`) sets `SISYPHUS_TEST_DB` against a Postgres service and runs the whole suite with `-p 1`. The shared `test` job cannot: its matrix spans macOS/Windows and the reusable workflow takes no service container. So a DB-backed test is only really covered if it skips on that one variable.
- The DB-backed suites share one database, so a suite must delete only its own fixtures on cleanup (scope by source prefix or table). Wiping a table deletes another package's rows mid-test. Packages also run concurrently, so literal fixture values (usernames, keys) can collide across suites on a shared unique column — use suite-distinct values, not just cleanup scoping.
- Locally: `docker run --rm -e POSTGRES_PASSWORD=test -e POSTGRES_USER=test -e POSTGRES_DB=test -p 5433:5432 postgres:17-alpine`, then `SISYPHUS_TEST_DB="postgres://test:test@127.0.0.1:5433/test?sslmode=disable" go test ./...`.

## Ingestion

`make ingest` (= `go run ./cmd/ssingest all`) runs incremental backfills for every
configured source, once, then exits. Per-source: `make ingest-git`, `make ingest-gitlab`,
`make ingest-jira`, `make ingest-telegram`.

### Ingestion topology

Ingestion has two halves with opposite scaling properties, split across the
`ingest.index` queue (`internal/indexjob`):

- **Fetch** (`ssingest serve`) is single-owner. It holds the git clone, the
  Telegram session and the source credentials, and it advances cursors — a
  single value two concurrent runs would interleave writes to, leaving the
  slower one to rewind it and re-fetch the same window forever. Each run takes a
  per-source Postgres advisory lock (`ingestrun.WithSourceLock`) so a one-shot
  `ssingest gitlab` cannot race the daemon; a contended run is skipped
  (`ErrLocked`), not failed. The lock covers the orphan prune too, which is
  equally read-modify-write.
- **Index** (`ssingest worker`) is stateless and scales with replicas. Chunk,
  embed and upsert are idempotent on `(source, source_id)`, so the worst a
  redelivery costs is repeated embedding work — and embedding is where the time
  goes. A worker needs no source access at all.

`ssingest serve` publishes rather than indexes, but by default **also runs a
worker in-process** (`ingest.worker.enabled`, default true) so a single-pod
install still works end to end. Turn it off once dedicated workers are deployed;
the Helm chart does that automatically when `ssingestWorker.enabled` is true.
The one-shot subcommands (`ssingest git`, `make ingest`) always index inline —
they must complete on their own with no worker running.

The publisher **filters before enqueuing**: it runs the same skip check
`pipeline.Index` does (`pipeline.Skipper`) and drops documents that would be
no-ops. This is not an optimization to trade away. A poll tick re-walks the
whole corpus and almost none of it has changed; enqueuing unfiltered would make
queue volume track corpus size rather than change, and `queue_jobs` rows are
currently never reclaimed (see the retention note under internal/queue).

`make ingest-serve` (= `go run ./cmd/ssingest serve`) instead runs as a daemon: it never
exits, and instead re-runs each source's incremental ingestion on a debounced
`internal/webhook.Trigger` fired either by that source's webhook HTTP endpoint (GitLab/Jira
only — `POST /webhooks/gitlab`/`/webhooks/jira` on `ingest.addr`, gated by
`gitlab.webhook.enabled`+`gitlab.webhook.secret` / `jira.webhook.*`) or by a per-source
`internal/webhook.Poller` ticker (`gitlab.poll.interval_seconds`, `jira.poll.interval_seconds`,
`ingest.git.poll.interval_seconds`, `ingest.files.poll.interval_seconds`,
`ingest.telegram.poll.interval_seconds` — each 0/unset disables that source's polling). A
webhook and a poll tick racing on the same source coalesce into one run instead of two
(`Trigger.Fire`'s debounce). This is the only ingestion scheduler in the deploy stack —
`cmd/ssapi` does not run any ingestion itself, so there's exactly one process racing to
write a given source's rows.

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

Kubernetes: `helm upgrade --install sisyphus deploy/helm/sisyphus -f my-values.yaml`.
The chart mirrors the compose stack. Three invariants it encodes: `ssingest`/`ssbot` are
single-replica with a `Recreate` strategy (two schedulers race on source rows, two bots
double-answer); `ssingestWorker` is the opposite — replicas>1, RollingUpdate, no PVC,
and enabling it flips `config.ingest.worker.enabled` to false so the scheduler stops
indexing in-process; and the sandbox is egress-denied by NetworkPolicy with ingress only from
its MCP front-end. Adding an MCP upstream (VictoriaLogs, Grafana, ...) is a values-only
change under `mcp.servers` — Deployment, Service and the `gateway.toml` `[[upstream]]`
are all generated from one entry.
