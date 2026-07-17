package pipeline

import (
	"github.com/go-faster/sisyphus/internal/ent"
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
func (p *Pipeline) unchanged(existing *ent.Document, doc index.Document) bool {
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
	if existing.ChunkerVersion != p.chunkerVersion {
		return false
	}
	return true
}

// chunkerVersion reports c's output version, or 0 if it does not declare one.
func chunkerVersion(c index.Chunker) int {
	if v, ok := c.(index.VersionedChunker); ok {
		return v.ChunkerVersion()
	}
	return 0
}
