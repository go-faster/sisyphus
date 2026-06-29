// Package git implements a git commit message chunker.
package git

import (
	"context"
	"maps"

	"github.com/go-faster/scpbot/internal/index"
)

// Chunker implements index.Chunker for git commit documents.
type Chunker struct{}

// New creates a new Chunker for git commits.
func New() *Chunker {
	return &Chunker{}
}

// Chunk turns an index.Document containing a git commit into ordered Chunks.
// For a commit document, we produce a single chunk with the commit message.
func (c *Chunker) Chunk(_ context.Context, doc index.Document) ([]index.Chunk, error) {
	if doc.Body == "" {
		return nil, nil
	}

	// Copy metadata from document to chunk
	chunkMeta := make(map[string]any)
	maps.Copy(chunkMeta, doc.Metadata)

	chunk := index.Chunk{
		ID:         index.NewID(),
		DocumentID: doc.ID,
		Index:      0,
		Type:       index.ChunkGitCommit,
		Title:      doc.Title,
		Text:       doc.Body,
		TextHash:   index.Hash(doc.Body),
		Metadata:   chunkMeta,
	}

	return []index.Chunk{chunk}, nil
}
