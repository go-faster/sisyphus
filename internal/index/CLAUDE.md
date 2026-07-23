# internal/index

**This package is the contract.** `Document`, `Chunk`, `Chunker`, `Embedder`, `Searcher`,
`Answerer`, `ContentResolver`, `URLFetcher`, `Link`, `Answer`, and the shared constants.

Two rules:

**Stay dependency-light.** stdlib + `github.com/google/uuid`, nothing else. Every other
package depends on this one; a dependency added here is a dependency added everywhere, and
it is how import cycles start. (`internal/notify` follows the same discipline for the same
reason.)

**Do not change a signature unilaterally.** Every implementer has to move with it. Update
the root `CLAUDE.md` and any affected nested `CLAUDE.md` in the same change.

## Things defined here that other packages rely on

- `index.Hash` — sha256 of normalized text. Content hashing goes through this, so re-embedding can be skipped when the hash is unchanged.
- Document identity is `(source, source_id)`, **not** `body_hash`.
- `index.VersionedChunker.ChunkerVersion()` — bump it when a chunker's output changes for input it already handled; the body hash cannot see a code change. A chunker declaring no version reports 0 and is never re-chunked.
- `index.Link.Valid()` — absolute http(s) URL + non-empty label. The link-button guarantee is built on this; see root `CLAUDE.md` § "Answers & link buttons".
