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

## A nil vector store must never reach Index

`Index`'s embed step is guarded on `p.vectors != nil`, so a nil store makes it write chunk
rows with no vectors, no error and no metric. Combined with the skip below — which does
not consider embedding state — those rows stay unembedded **forever**: returned by
Postgres FTS, invisible to vector search, until the body changes or someone runs
`--reset`. That was #125.

`wire.Services.Vectors` is therefore never nil. It connects on use rather than at startup,
so "Qdrant is down" arrives as an error from `Upsert` — which `Index` returns *before*
persisting anything, so the document is retried whole and indexes correctly once Qdrant is
back, with no restart. Search still degrades to FTS on a missing store; indexing must not,
because a document indexed with no vectors is one nobody can find.

The nil guard stays for tests that deliberately construct a pipeline without a store. Do
not reintroduce a production path that passes one.

## The document-level skip must cover every input

`skip.go`'s `unchanged` must consider **every input that shapes the output**: body hash,
`doc.URL` (propagated onto chunks as `source_url`), and the chunker version.

Anything left out is a field that can change while indexing says "unchanged" forever. A
document's body is the only thing that normally moves, so nothing else ever forces a
revisit — the omission is permanent, not eventual.

It deliberately does **not** consider whether the chunks were embedded, which is why a
document indexed without a vector store never healed itself (#125). Adding that check
would put a per-chunk query on a path every poll tick runs over the whole corpus — the
exact cost `Skipper` exists to avoid. The fix belongs at the write (never index without a
store) and in `ssingest repair` (rebind rows already in that state), not here.

`pipeline.Skipper` answers the same question without doing the work, for a producer
filtering documents before they cost a queue row. It **shares** `unchanged` with `Index`
rather than reimplementing it, because a producer that under-reports change silently stops
indexing.

## The stale-point delete runs after commit

`VectorStore` is `Upsert` + `Delete`. The stale-point delete runs **after** the ent tx
commits, so it cannot be rolled back: it retries (`deleteStaleVectors`), and on final
failure leaks orphaned points that only `ssingest gc` can reclaim.

Non-fatal by design — the document is indexed correctly either way.
