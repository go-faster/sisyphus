# internal/indexjob

The boundary between ingestion's two halves: `Kind` (which chunker), `Payload` (one
document per job), `Publisher` (producer side) and `Handler` (worker side), over the
`ingest.index` queue.

A job carries the **document**, not a reference to it. That is what lets a worker run with
no source credentials, no git clone, and no Telegram session file.

## Two load-bearing details

**1. `Decode` rehydrates metadata.** The GitLab and Jira ingesters put a concrete Go
struct in `index.Document.Metadata` (a `map[string]any`), and their chunkers recover it by
type assertion. After a JSON round-trip it is a `map[string]any`, the assertion fails, and
the chunker falls through to its untyped fallback: one flat chunk instead of typed
summary/comment chunks — with **no error and no log**. Every issue and MR would index
worse forever, and only `--reset` would ever reach them.

A new struct-valued metadata key needs a case in `rehydrate`. `TestRoundTripChunks` is the
only thing that catches one that was not added.

**2. `Canonicalize` puts the INLINE path through the same JSON normalization**, because a
metadata value's Go type is observable downstream: `internal/search/qdrant` maps an `int`
to a Qdrant integer and a `float64` to a double, and drops a `[]string` entirely, while a
`[]any` of the same strings converts fine. Without it, `ssingest gitlab` and
`ssingest serve` would write different Qdrant payloads for the same document.

Inert for retrieval today — every condition is a keyword match — but do not let the two
paths diverge again.

## Publisher does not dedup by content

Keying on `(source, source_id, body_hash)` looks right and is wrong: queue dedup covers a
job's whole lifetime, so a document edited A→B and reverted to A finds its key already
spent and is never re-indexed.

It publishes under a fresh key and relies on `pipeline.Index` being idempotent instead.
