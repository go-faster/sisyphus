# internal/vectorgc

`ssingest gc`: deletes vector points that no chunk references. Postgres is the source of
truth.

## Never collapse the two passes

The two passes are separated by `Grace`, and that gap is the whole point.

`pipeline.Index` upserts points **before** committing chunk rows, so a document mid-index
looks exactly like an orphan. Deleting one is unrecoverable: the chunk row then lands with
`qdrant_point_id` already set, so it is never re-embedded, and vector search can never
reach it again.

A single-pass gc is therefore a data-loss bug that only shows up under concurrent
ingestion.
