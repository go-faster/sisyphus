# internal/vectorrepair

`ssingest repair`: re-embeds chunks whose vector point is keyed by the wrong ID
(`chunks.id != qdrant_point_id`), so vector hits can hydrate their text from Postgres
again. See `internal/pipeline/CLAUDE.md` for why that invariant matters.

Runs weekly under `ssingest maint` (`maintenance.repair.*`), deliberately far less often
than gc: every repaired row costs an embedding call, and that capacity is shared with
ingestion. Indexing no longer produces the drift, so this is cleanup of old rows, not an
ongoing correction.

## Write before rebind, delete after

Order is load-bearing: write the **new** point, then rebind the row, then delete the old
point.

An interrupted run therefore leaves an orphaned point — which is `ssingest gc`'s job — and
never a row pointing at a point that does not exist, which nothing can repair.
