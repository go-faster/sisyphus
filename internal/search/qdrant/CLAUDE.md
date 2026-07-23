# internal/search/qdrant

Qdrant client + `index.Searcher`. Also implements `pipeline.VectorStore` (`Upsert` +
`Delete` by point ID).

## Payload conversion must not swallow errors

`chunkToPayload` converts chunk metadata into the point payload and **returns the keys it
could not convert**, which `Upsert` logs. Do not go back to swallowing that error.

`qdrant.NewValue` is a closed switch over JSON primitives plus `map[string]any`/`[]any`,
and `index.Chunk.Metadata` is a `map[string]any`. So a `[]string` — which is exactly what
GitLab/Jira labels and components are — used to vanish from the payload with **no error and
no log**. The only symptom was a keyword filter that matched nothing, forever.

`sanitizePayloadValue` now lists the primitives explicitly (an `int` must stay a Qdrant
integer, not become a double) and normalizes everything past them through JSON.

Related: `internal/indexjob`'s `Canonicalize` exists so the inline and queued ingestion
paths produce the same payload types for the same document.
