# internal/vectorrepair

`ssingest repair`: re-embeds chunks that are not bound to a vector point of their own, so
vector hits can hydrate their text from Postgres again. See `internal/pipeline/CLAUDE.md`
for why that invariant matters.

## Two ways to be unbound, one repair

`unbound()` matches both, and the second half is easy to delete by accident:

- **`qdrant_point_id != id`** — bound to someone else's point, which hydrates to empty text. The chunk stays searchable and contributes nothing.
- **`qdrant_point_id IS NULL`** — never embedded at all. Invisible to vector search, still returned by Postgres FTS.

The null case is not hypothetical: a process that started while Qdrant was unreachable
used to write exactly these rows (#125), and **nothing else in the system revisits them**.
The document-level skip compares body hash, URL and chunker version — never embedding
state — so on every later run the document is "unchanged" and its chunks are never
reconsidered. Before this, `--reset` was the only way back.

The write path no longer produces them (`wire`'s indexing store connects on use, so a
Qdrant outage fails the document instead of silently skipping the embed), but rows written
before that fix are only reachable here.

Runs weekly under `ssingest maint` (`maintenance.repair.*`), deliberately far less often
than gc: every repaired row costs an embedding call, and that capacity is shared with
ingestion. Indexing no longer produces the drift, so this is cleanup of old rows, not an
ongoing correction.

## Write before rebind, delete after

Order is load-bearing: write the **new** point, then rebind the row, then delete the old
point.

An interrupted run therefore leaves an orphaned point — which is `ssingest gc`'s job — and
never a row pointing at a point that does not exist, which nothing can repair.
