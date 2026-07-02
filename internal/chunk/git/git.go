// Package git implements a git commit message chunker.
package git

import (
	"context"
	"maps"
	"strings"

	"github.com/go-faster/sisyphus/internal/index"
)

// Chunker implements index.Chunker for git commit documents.
type Chunker struct{}

// New creates a new Chunker for git commits.
func New() *Chunker {
	return &Chunker{}
}

// Chunk turns an index.Document containing a git commit or tag into ordered Chunks.
// For a commit or tag document, we produce a single chunk with the message.
func (c *Chunker) Chunk(_ context.Context, doc index.Document) ([]index.Chunk, error) {
	if doc.Body == "" {
		return nil, nil
	}

	// Copy metadata from document to chunk
	chunkMeta := make(map[string]any)
	maps.Copy(chunkMeta, doc.Metadata)

	// Determine chunk type based on source prefix
	chunkType := index.ChunkGitCommit
	if strings.HasPrefix(string(doc.Source), index.SourceGitTagsPrefix) {
		chunkType = index.ChunkGitTag
	}

	chunk := index.Chunk{
		ID:         index.NewID(),
		DocumentID: doc.ID,
		Index:      0,
		Type:       chunkType,
		Title:      doc.Title,
		Text:       doc.Body,
		TextHash:   index.Hash(doc.Body),
		Metadata:   chunkMeta,
	}

	return []index.Chunk{chunk}, nil
}
