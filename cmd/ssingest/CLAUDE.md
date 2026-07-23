# cmd/ssingest

Ingestion CLI and daemon. Wires its dependencies inline — it does **not** reuse
`internal/wire` beyond `wire.NewServices`.

## Subcommands

| Command | What it does |
|---|---|
| `git`, `files`, `gitlab`, `jira`, `telegram`, `all` | one-shot incremental run, then exit |
| `serve` | long-lived daemon: webhooks + pollers trigger the same per-source runs |
| `worker` | drains the `ingest.index` queue and does nothing else |
| `index` | index documents directly |
| `notify` | one pass of the notify collectors + dispatcher (`internal/notify`) |
| `gc` | sweep vector points no chunk references (`internal/vectorgc`) |
| `repair` | rebind chunks whose point is keyed by the wrong ID (`internal/vectorrepair`) |

`gc` and `repair` are not ingestion; they just live here because this is the binary that
already holds the Qdrant and Postgres clients.

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

`cmd/ssapi` runs no ingestion, so exactly one process races to write a given source's rows.

Trigger keys: `gitlab`, `jira`, `git`, `files`, `telegram`, `notify` (see `cmd_serve.go`).

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

The notify collectors keep their own cursors under distinct sources (`notify_gitlab`,
`notify_jira`) so notify's poll cadence and diff state never interact with ingestion's.
