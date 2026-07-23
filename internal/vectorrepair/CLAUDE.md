# internal/vectorrepair

`ssingest repair`: re-embeds chunks whose vector point is keyed by the wrong ID
(`chunks.id != qdrant_point_id`), so vector hits can hydrate their text from Postgres
again. See `internal/pipeline/CLAUDE.md` for why that invariant matters.

## Write before rebind, delete after

Order is load-bearing: write the **new** point, then rebind the row, then delete the old
point.

An interrupted run therefore leaves an orphaned point — which is `ssingest gc`'s job — and
never a row pointing at a point that does not exist, which nothing can repair.
