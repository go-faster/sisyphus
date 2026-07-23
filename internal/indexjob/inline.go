package indexjob

import (
	"context"

	"github.com/go-faster/sisyphus/internal/index"
	"github.com/go-faster/sisyphus/internal/pipeline"
)

// Inline wraps an indexer that runs in the fetching process, so a document
// indexed there takes exactly the shape it would have taken through the queue.
//
// Without it the one-shot subcommands and `ssingest serve` would write
// different Qdrant payloads for the same document — see [Canonicalize].
func Inline(p pipeline.Indexer) pipeline.Indexer { return inlineIndexer{p: p} }

type inlineIndexer struct{ p pipeline.Indexer }

func (i inlineIndexer) Index(ctx context.Context, doc index.Document) error {
	doc, err := Canonicalize(doc)
	if err != nil {
		return err
	}
	return i.p.Index(ctx, doc)
}
