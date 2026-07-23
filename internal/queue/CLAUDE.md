# internal/queue

Shared substrate for background work: notify delivery, agent investigations, and ingest
indexing. One `queue_jobs` table serves every queue, distinguished by the `queue` column,
so there is one claim path and one set of indexes.

`Postgres.Fetch` claims with `FOR UPDATE SKIP LOCKED`, so N workers drain concurrently
without coordination. `Nack` retries with backoff until `MaxAttempts`, after which the job
is terminal (`status=error`) and stays for inspection.

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

## Not yet solved: retention

Rows are never deleted and every job costs 2+ `UPDATE`s, so the table accumulates dead
tuples with no retention, archiving, partitioning or autovacuum tuning. Every comparable
system treats this as mandatory (pgq/pgq: append-only + `TRUNCATE` rotation with
`autovacuum_enabled=off`; pgmq: delete on ack or archive; dataddo/pgq: partitions via
pg_partman). Fix before this carries real volume.
