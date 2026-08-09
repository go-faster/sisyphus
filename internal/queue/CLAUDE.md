# internal/queue

Shared substrate for background work: notify delivery, agent investigations, and ingest
indexing. One `queue_jobs` table serves every queue, distinguished by the `queue` column,
so there is one claim path and one set of indexes.

`Postgres.Fetch` claims with `FOR UPDATE SKIP LOCKED`, so N workers drain concurrently
without coordination. `Nack` retries with backoff until `MaxAttempts`, after which the job
is terminal (`status=error`) and stays for inspection.

`ReapStale` and `Purge` are **not** per-queue duties, even though they are per-queue calls:
`ssingest maint` runs both across every queue in the system (`allQueues` in
`cmd/ssingest/cmd_maint.go`). A new queue that is not added there is never reaped and never
purged — which is how notify delivery went unreaped until #66.

## Three load-bearing decisions — do not undo

Each was a measured or prior-art-confirmed mistake before it was fixed.

**1. `visible_at` is ONE column** serving both "claimable at" and "claim expires at".
Split into `available_at`/`lease_expires_at`, the `OR` between them defeats index ordering
and Postgres sorts every matching row before `LIMIT`: measured **76ms and a 9.4MB external
merge sort per claim** on a 200k backlog, versus 0.06ms after. The partial index
`(queue, visible_at) WHERE status IN ('pending','running')` serves both the filter and the
`ORDER BY`, and holds only outstanding work — terminal jobs leave the index, so history
costs claims nothing (1.3MB vs 34MB for the full index on the same table).

**2. Time comes from POSTGRES, not from Go.** Queries say `COALESCE($n, now())` and
`PostgresOptions.Now` is nil outside tests. Per-process clocks mean a replica running fast
sees live claims as expired and steals them. (pgmq uses `clock_timestamp()`, dataddo/pgq
`CURRENT_TIMESTAMP` — nobody sane uses client time.)

**3. A handler's deadline is the CLAIM's deadline** (`Delivery.Deadline`), not a separate
configured timeout. There is deliberately no `WorkerOptions.JobTimeout`: two independent
knobs drift, and a handler outliving its claim means two workers run the same job.

## What the interface deliberately does not promise

It carries **payloads** and acks by ID — never rows, table names, or transactions — so a
broker-backed implementation stays possible. Two consequences, which must not be designed
away:

- Dedup is best-effort. Delivery is at-least-once; consumers must be idempotent.
- Transactional enqueue is `Postgres.WithTx`'s guarantee, **not** the interface's.

Job state of record (a report, a delivery outcome) belongs on the domain row, never here.
A queue answers "what work is outstanding", never "what happened to job X".

`queue.Worker` is the drain loop (claim → run → ack/nack). It claims only as many jobs as
it has free slots, so a backlog never sits claimed behind a busy handler.

## Message.ID / Message.Key

Producers that keep a domain row set `Message.ID` to that row's ID so the two share an
identifier. Set `Message.Key` to that ID too **unless the queue is genuinely the dedup
point** — a queue job outlives the row it refers to, so reusing a business dedup key
silently swallows a re-enqueue after the old row is cleaned up.

## Retention: delete settled rows, in two windows

`Postgres.Purge` deletes terminal rows past their window, run hourly by `ssingest maint`
(`maintenance.queue_retention.*`). **`done` after 72h, `error` after 30d** — an
acknowledged job is an audit trail nobody reads, a failed one is why an operator opens the
table at all. Either window at `0` keeps that status forever.

Three properties that are load-bearing, not incidental:

- **Only terminal rows, only by `completed_at`.** A pending, running or backing-off job is
  outstanding work at any age. The purge predicate must never widen to `created_at`.
- **Batched, and capped per sweep.** An unbounded `DELETE` on a churn table takes a
  long-lived lock and bloats WAL — dataddo/pgq warns about exactly this. `Capped` in the
  report means the sweep stopped early; persistently capped means the batch is too small.
- **Deleting terminal rows is safe for dedup *only because* every producer publishes under
  a fresh or domain-row key** (`indexjob`, `agentstore`, `notify/store` all use
  `Key: id.String()`). `Publish`'s dedup covers a row's whole lifetime, so a producer that
  used the queue as its own idempotency record would have that record silently deleted by
  retention. If you add a producer that keys by business identity, retention becomes its
  correctness problem — see `internal/indexjob/CLAUDE.md`, which rejects exactly that key.

Deliberately **not** archived or partitioned. An archive table keeps rows nobody queries;
partitioning (`pg_partman`, as dataddo/pgq recommends) is the right answer at volume and
stays the escape hatch, but it is real operational setup for a table measured in hundreds
of rows a week. Payload-nulling on ack — free, since `Ack` already `UPDATE`s the row — is
the other cheap lever if bytes rather than rows turn out to be the problem.

The table also carries `fillfactor=90` and `autovacuum_*_scale_factor=0.02` (see
`migrations/20260809120000_churn_table_storage.sql`): the default 0.2 waits for a fifth of
a churn table to be dead before vacuuming, and no free space per page means a claim can
never be a HOT update.
