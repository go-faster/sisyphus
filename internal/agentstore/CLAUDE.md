# internal/agentstore

ent-backed persistence for `cmd/ssagent`'s `InvestigationJob` rows (pending/running/done/
error, `idempotency_key`, `Report` snapshot), plus dispatch through `internal/queue` on
queue `agent.investigate`.

## Submit is the dedup point

A repeated `idempotency_key` returns the **existing** job instead of creating a second one.
The row and its queue job are written in **one tx**, so a submitted job is never queued
twice, and never accepted without being queued.

## ReapStale must not sweep everything

`Store.ReapStale` settles **only** jobs the queue abandoned: attempts spent, no live lease.

It must never sweep all `pending`/`running` rows. With a shared queue, "running" means
"running on some replica" — a blanket sweep reports live investigations as dead.
