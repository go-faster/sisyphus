# internal/pipeline

`Pipeline.Index`: idempotent doc+chunk upsert (ent) + embed (Ollama) + vector
Upsert/Delete (Qdrant). Per-chunk embedding skip preserves unchanged chunks'
`qdrant_point_id`; changed documents get their stale points cleaned up.

## INVARIANT: a chunk's vector point is keyed by the chunk's own ID

`chunks.id == chunks.qdrant_point_id`. Retrieval hydrates a vector hit's text from
Postgres **by chunk ID**, so a point stored under any other ID resolves to empty text
forever.

`Index` enforces this by adopting the *existing row's* ID when a chunk matches
`(index, text_hash)` — `persist`'s upsert keeps the row's ID on conflict, so embedding
under the chunker's freshly generated UUID would break it.

The stale cleanup deletes the point ID the row **recorded**, not the row's own ID — using
the row's ID misses the real point on rows that drifted before this was enforced. It also
drops stale rows whether or not they were ever embedded, because never-embedded leftovers
stay visible to Postgres FTS otherwise.

`internal/vectorrepair` repairs rows that already drifted.

## The document-level skip must cover every input

`skip.go`'s `unchanged` must consider **every input that shapes the output**: body hash,
`doc.URL` (propagated onto chunks as `source_url`), and the chunker version.

Anything left out is a field that can change while indexing says "unchanged" forever. A
document's body is the only thing that normally moves, so nothing else ever forces a
revisit — the omission is permanent, not eventual.

`pipeline.Skipper` answers the same question without doing the work, for a producer
filtering documents before they cost a queue row. It **shares** `unchanged` with `Index`
rather than reimplementing it, because a producer that under-reports change silently stops
indexing.

## The stale-point delete runs after commit

`VectorStore` is `Upsert` + `Delete`. The stale-point delete runs **after** the ent tx
commits, so it cannot be rolled back: it retries (`deleteStaleVectors`), and on final
failure leaks orphaned points that only `ssingest gc` can reclaim.

Non-fatal by design — the document is indexed correctly either way.
