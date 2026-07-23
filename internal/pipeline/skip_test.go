package pipeline

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/index"
)

// versionedChunker is testChunker with a declared, settable output version.
type versionedChunker struct {
	testChunker
	version int
}

func (c versionedChunker) ChunkerVersion() int { return c.version }

func skipperDoc() index.Document {
	return index.Document{
		ID:       uuid.New(),
		Source:   index.Source(testSourcePrefix + "skipper"),
		SourceID: testSourcePrefix + "skipper/doc-1",
		URL:      "https://example.com/doc-1",
		Title:    "Doc",
		Body:     "alpha beta",
	}
}

// TestSkipperMatchesPipeline is the property that matters: a producer filtering
// with Skipper must drop exactly the documents Index would have skipped. If it
// drops more, those documents are never indexed and nothing reports it.
func TestSkipperMatchesPipeline(t *testing.T) {
	client := openTestDB(t)
	cleanDB(t, client)
	t.Cleanup(func() { cleanDB(t, client) })

	ctx := context.Background()
	store := &fakeVectorStore{}
	p := newTestPipeline(t, client, store)
	s := NewSkipper(client, testChunker{})

	doc := skipperDoc()

	// Never indexed: not unchanged.
	got, err := s.Unchanged(ctx, doc)
	require.NoError(t, err)
	require.False(t, got, "an unseen document is always changed")

	require.NoError(t, p.Index(ctx, doc))

	// Indexed and untouched: unchanged.
	got, err = s.Unchanged(ctx, doc)
	require.NoError(t, err)
	require.True(t, got)

	// Body moved.
	changed := doc
	changed.Body = "alpha gamma"
	got, err = s.Unchanged(ctx, changed)
	require.NoError(t, err)
	require.False(t, got, "a changed body must not be skipped")

	// URL moved: not part of the body, but propagated onto chunks as
	// source_url, so it has to re-chunk.
	moved := doc
	moved.URL = "https://example.com/moved"
	got, err = s.Unchanged(ctx, moved)
	require.NoError(t, err)
	require.False(t, got, "a changed URL must not be skipped")

	// Chunker version moved: same body, different code splitting it.
	bumped := NewSkipper(client, versionedChunker{version: 7})
	got, err = bumped.Unchanged(ctx, doc)
	require.NoError(t, err)
	require.False(t, got, "a bumped chunker version must not be skipped")
}

// TestSkipperFillsBodyHash pins that a caller may hand over a document whose
// hash the pipeline would have computed, as the walkers do.
func TestSkipperFillsBodyHash(t *testing.T) {
	client := openTestDB(t)
	cleanDB(t, client)
	t.Cleanup(func() { cleanDB(t, client) })

	ctx := context.Background()
	p := newTestPipeline(t, client, &fakeVectorStore{})
	s := NewSkipper(client, testChunker{})

	doc := skipperDoc()
	require.Empty(t, doc.BodyHash)
	require.NoError(t, p.Index(ctx, doc))

	got, err := s.Unchanged(ctx, doc)
	require.NoError(t, err)
	require.True(t, got, "an unset BodyHash must be computed, not compared as empty")
}
