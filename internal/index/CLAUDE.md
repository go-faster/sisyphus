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
- `index.Link.Valid()` — absolute http(s) URL + non-empty label. The link-button guarantee is built on this; see `internal/notify/CLAUDE.md` § Buttons.

## Two clocks, and only one of them is ours

`Document.CreatedAt`/`UpdatedAt` are the **source object's** timestamps; `captured_at` on
the stored row is **ours**, set by the pipeline on every index.

Most sources have no timestamp to report — git tracks commits, not a file's birth — so
every `git_docs`/`git_code`/`git_manifest` document carries a zero `created_at`. That is
correct, not corruption: on one production corpus it is ~46% of documents, and exactly the
file-derived ones. Only GitLab, Jira and git-commit adapters populate them.

The trap: a zero is stored as `0001-01-01`, not NULL (the column is `Optional` but not
`Nillable`), so `WHERE created_at < <anything>` quietly matches every one of them. It has
already produced one confidently wrong answer during an investigation. **Filter on
`captured_at` whenever the question is "when did we index this".**

Making the column `Nillable` would remove the sentinel, at the cost of `*time.Time` through
every writer plus a data migration. Nothing reads `created_at` today, so it has not been
worth it — revisit if ranking or filtering ever uses recency.
