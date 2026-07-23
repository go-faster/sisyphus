package pipeline

import (
	"context"

	"github.com/go-faster/errors"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/ent/document"
	"github.com/go-faster/sisyphus/internal/index"
)

// unchanged reports whether re-indexing doc would produce what the stored row
// already holds, in which case Index can skip it.
//
// The check must cover every input that shapes the output, not just the body.
// Anything it omits is a field that can change while indexing keeps saying
// "unchanged" — and because a document's body is what changes on re-ingest,
// nothing else ever forces the row to be revisited. The omission is permanent,
// not eventual.
func unchanged(existing *ent.Document, doc index.Document, chunkerVersion int) bool {
	// Body: the document's content.
	if existing.BodyHash != doc.BodyHash {
		return false
	}
	// URL: not part of the body, but propagated onto every chunk as source_url
	// (see Index), so a changed URL has to re-chunk. It only moves when a
	// source's base URL is reconfigured — and then nothing else about the
	// document moves with it, so this is the only thing that can catch it.
	if existing.SourceURL != doc.URL {
		return false
	}
	// Chunker: same body, different code splitting it.
	if existing.ChunkerVersion != chunkerVersion {
		return false
	}
	return true
}

func (p *Pipeline) unchanged(existing *ent.Document, doc index.Document) bool {
	return unchanged(existing, doc, p.chunkerVersion)
}

// chunkerVersion reports c's output version, or 0 if it does not declare one.
func chunkerVersion(c index.Chunker) int {
	if v, ok := c.(index.VersionedChunker); ok {
		return v.ChunkerVersion()
	}
	return 0
}

// Skipper answers [Pipeline.Index]'s skip question without doing any of the
// work: whether indexing doc would produce exactly what is already stored.
//
// It exists so a producer that hands documents to a worker instead of indexing
// them itself can drop the unchanged ones before they cost a queue row. A
// re-walk of a source normally finds almost nothing changed, so without this
// filter every poll tick would enqueue the entire corpus and the queue's volume
// would track corpus size rather than change.
//
// It deliberately shares [unchanged] with the pipeline rather than
// reimplementing the comparison. Two copies would drift, and a producer that
// under-reports change is indistinguishable from a document that never updates.
type Skipper struct {
	db             *ent.Client
	chunkerVersion int
}

// NewSkipper builds a Skipper for documents destined for chunker. chunker must
// be the same one the eventual [Pipeline] uses, or the chunker-version half of
// the comparison is meaningless.
func NewSkipper(db *ent.Client, chunker index.Chunker) *Skipper {
	return &Skipper{db: db, chunkerVersion: chunkerVersion(chunker)}
}

// Unchanged reports whether indexing doc would be a no-op. A document with no
// stored row is always changed.
func (s *Skipper) Unchanged(ctx context.Context, doc index.Document) (bool, error) {
	if doc.BodyHash == "" {
		doc.BodyHash = index.Hash(doc.Body)
	}
	existing, err := s.db.Document.Query().
		Where(
			document.Source(string(doc.Source)),
			document.SourceID(doc.SourceID),
		).
		// Only the fields unchanged compares. Selecting the whole row would
		// pull every document's body back on every poll, which for a source
		// that rarely changes is the dominant cost of the walk.
		Select(
			document.FieldBodyHash,
			document.FieldSourceURL,
			document.FieldChunkerVersion,
		).
		Only(ctx)
	if ent.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, errors.Wrap(err, "query document")
	}
	return unchanged(existing, doc, s.chunkerVersion), nil
}
