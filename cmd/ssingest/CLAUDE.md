# cmd/ssingest

Ingestion CLI and daemon. Wires its dependencies inline — it does **not** reuse
`internal/wire` beyond `wire.NewServices`.

## Subcommands

| Command | What it does |
|---|---|
| `git`, `files`, `gitlab`, `jira`, `telegram`, `all` | one-shot incremental run, then exit |
| `serve` | long-lived daemon: webhooks + pollers trigger the same per-source runs |
| `worker` | drains the `ingest.index` queue and does nothing else |
| `maint` | long-lived daemon: runs `gc` and `repair` on a schedule (`internal/maint`) |
| `index` | index documents directly |
| `gc` | sweep vector points no chunk references (`internal/vectorgc`) |
| `repair` | rebind chunks whose point is keyed by the wrong ID (`internal/vectorrepair`) |

`gc`, `repair` and `maint` are not ingestion; they just live here because this is the
binary that already holds the Qdrant and Postgres clients.

## maint: the schedule lives in a process

ARCHITECTURE.md wants gc and repair as CronJobs. Compose has no such object, so `maint` is
a daemon whose only job is a timer, and a Postgres advisory lock per job
(`internal/pglock`, keyed `maint/<job>`) does what `concurrencyPolicy: Forbid` would.

**The one-shot `gc`/`repair` subcommands take the same lock**, so running one by hand
while the daemon is up is safe — the contended run logs a skip and exits 0. Both paths
also call the same job body in `internal/maint`, so there is one implementation of what a
sweep does, not two.

It is a **separate deployment from `serve`**, not a goroutine inside it. `repair`
re-embeds, and `serve` is the process operators are told to keep clear of embedding work
once `ssingest worker` replicas exist; sharing a lifecycle would walk that back for the
one job that most looks like ingestion load.

Jobs fire `maintenance.start_delay` after startup rather than one interval in, because a
deployment that restarts daily would otherwise never reach a daily job. The delay is what
keeps a crash loop from re-scanning the vector store on every restart. Shutdown cancels an
in-flight sweep and waits at most `maintenance.drain_timeout` — a sweep holds no state,
and the next run re-finds whatever was left.

Interval `0` disables a job. A `maint` process with everything disabled idles rather than
exits, so a config change does not turn into a crash-looping container.

## A broken dependency fails the run, never the process

`maint` never exits because a store is unavailable. A Qdrant outage makes a sweep fail —
logged, counted by `sisyphus.maint.runs{status="error"}` — and the next tick retries.
Restarting the pod would not have fixed it, and a crash-looping container hides the
outage behind a symptom that looks like a bug in the daemon.

This is why the jobs call `wire.NewVectorStore` **per sweep** instead of using
`Services.Vectors`. That one is decided once at startup: `wire.NewServices` degrades to a
nil store when Qdrant is unreachable and never retries, so a daemon that happened to start
during an outage would never garbage-collect again, however long it ran afterwards. Do not
"optimize" this back into a store resolved once — the connection is not the expensive part
of a daily sweep, and reusing it converts a maintenance gap into a maintenance stoppage.

`sisyphus.maint.runs{job,status}` is the whole alerting surface: failures are
`increase(...{status="error"}[1h]) > 0`, and "maintenance stopped happening at all" is
`increase(...{status="ok"}[2 * interval]) == 0`. Both are already in the counter — resist
adding a companion gauge for it, which buys nothing and only complicates the alert.

## Topology: two halves, opposite scaling

Split across the `ingest.index` queue (`internal/indexjob`).

**Fetch** (`serve`) is **single-owner**. It holds the git clone, the Telegram session and
the source credentials, and it advances cursors — a single value that two concurrent runs
would interleave writes to, leaving the slower one to rewind it and re-fetch the same
window forever. Each run takes a per-source Postgres advisory lock
(`ingestrun.WithSourceLock`) so a one-shot `ssingest gitlab` cannot race the daemon; a
contended run is **skipped** (`ErrLocked`), not failed. The lock covers the orphan prune
too, which is equally read-modify-write.

**Index** (`worker`) is **stateless and scales with replicas**. Chunk, embed and upsert are
idempotent on `(source, source_id)`, so the worst a redelivery costs is repeated embedding
work — and embedding is where the time goes. A worker needs no source access at all.

`serve` publishes rather than indexes, but by default **also runs a worker in-process**
(`ingest.worker.enabled`, default true) so a single-pod install works end to end. Turn it
off once dedicated workers are deployed; the Helm chart does that automatically when
`ssingestWorker.enabled` is true. The one-shot subcommands always index **inline** — they
must complete on their own with no worker running.

## The publisher filters before enqueuing

It runs the same skip check `pipeline.Index` does (`pipeline.Skipper`) and drops documents
that would be no-ops. **This is not an optimization to trade away.** A poll tick re-walks
the whole corpus and almost none of it has changed; enqueuing unfiltered would make queue
volume track corpus *size* rather than *change* — and `queue_jobs` rows are currently never
reclaimed (see `internal/queue/CLAUDE.md`).

## serve: the only ingestion scheduler

It never exits. Each source's incremental run fires off a debounced
`internal/webhook.Trigger`, driven either by that source's webhook endpoint (GitLab/Jira
only — `POST /webhooks/gitlab`, `/webhooks/jira` on `ingest.addr`, gated by
`gitlab.webhook.enabled`+`secret` / `jira.webhook.*`) or by a per-source
`internal/webhook.Poller` ticker. A webhook and a poll tick racing on the same source
coalesce into one run (`Trigger.Fire`'s debounce).

`POST /webhooks/alertmanager` (gated by `alertmanager.webhook.enabled`, authenticated by
`alertmanager.webhook.token`) is the exception: there is no fetcher to wake, so the
handler decodes the body into alert events and routes them inline before answering 202 —
Alertmanager retries a non-2xx, and every destination is idempotent on `Event.ID`. With
`alertmanager.investigate.enabled`, the agent destination submits an investigation per
firing alert, keyed by the event ID so a repeat_interval resend reuses the existing job.

`cmd/ssapi` runs no ingestion, so exactly one process races to write a given source's rows.

Trigger keys: `gitlab`, `jira`, `git`, `files`, `telegram` (see `cmd_serve.go`). There is
no `notify` key: notifications are a *destination* of the gitlab/jira runs now, not a
source of their own — see "One poll, two destinations" below.

## Flags

- `--reset <src|all>` wipes the source end-to-end: in one ent Tx it deletes `documents`, `chunks` and `SyncState` for that source (chunk IDs captured pre-delete), commits, then `qdrant.Delete` frees the point IDs. `--reset all` refuses without `--yes-i-mean-all`. For git, resetting "all" also resets per-repo docs and commits sources.
- `--since <RFC3339>` overrides cursors (Jira `LastUpdated`, GitLab `UpdatedAfter`).
- `--limit <int>` caps documents per source.
- `--dry-run` fetches and logs counts without indexing.

## Sync state

Each source (per-repo for git, per-resource-type for gitlab REST) has a `SyncState` row:
`source`, `last_synced_at`, `last_cursor` (opaque JSON), `status`, `error`,
`document_count`. The cursor is read before the run and written back per batch (jira,
gitlab pagination) or per repo (git commits), so a partial run resumes.

## One poll, two destinations

The GitLab and Jira runs are **source adapters**: each fetched item becomes both an
`index.Document` (handed to the indexer) and a canonical `event.Event`
(`ingest/gitlab.EventFromMergeRequest`, `ingest/jira.EventFromIssue`), routed through
`ingestrun.Runner.Router` — the `event.Mux` built in `router.go`, with the notification
gateway subscribed per source. Notify used to run a second fetcher over the same REST
APIs on its own `notify_gitlab`/`notify_jira` cursors; those are gone, along with the
`notify` subcommand and trigger. Rows for the old sources may still sit in `sync_states`;
nothing reads them.

Two consequences worth knowing:

- **A routing failure holds the cursor back.** Indexing and routing are both idempotent,
  but a cursor advanced past a window no destination was told about loses that
  notification permanently, so a failed route pins the cursor and the window is
  re-fetched. `--dry-run` routes nothing.
- **`--reset` does not spam notifications.** The projected `EventID`s are keyed by
  (object, recipient) with no timestamp, so re-emitting a re-fetched MR hits the outbox
  `DedupKey` and produces nothing.
