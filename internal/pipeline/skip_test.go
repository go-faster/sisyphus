package pipeline

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/go-faster/sisyphus/internal/index"
)

// versionedChunker is testChunker with a declared output version.
type versionedChunker struct {
	testChunker
	version int
}

func (v versionedChunker) ChunkerVersion() int { return v.version }

var _ index.VersionedChunker = versionedChunker{}

func TestChunkerVersion(t *testing.T) {
	if got := chunkerVersion(testChunker{}); got != 0 {
		t.Errorf("a chunker that declares no version: got %d, want 0", got)
	}
	if got := chunkerVersion(versionedChunker{version: 7}); got != 7 {
		t.Errorf("a versioned chunker: got %d, want 7", got)
	}
}

// TestSkipRechunksWhenChunkerVersionChanges is the point of the version: the
// body is byte-identical, so the body hash cannot tell that the code splitting
// it has changed. Without this the document keeps chunks built by the old
// chunker until someone runs a full --reset.
func TestSkipRechunksWhenChunkerVersionChanges(t *testing.T) {
	client := openTestDB(t)
	defer cleanDB(t, client)

	ctx := context.Background()
	doc := index.Document{
		ID:       uuid.New(),
		Source:   index.SourceGitDocs("test/chunkerver"),
		SourceID: "test/chunkerver",
		Title:    "Test",
		Body:     "one\n===\ntwo",
	}

	v1 := &fakeVectorStore{}
	p1, err := New(client, versionedChunker{version: 1}, &fakeEmbedder{}, v1, PipelineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := p1.Index(ctx, doc); err != nil {
		t.Fatalf("v1 index: %v", err)
	}
	if len(v1.upserted) == 0 {
		t.Fatal("v1 should have embedded the document")
	}

	// Same body, same pipeline version: skipped.
	v1again := &fakeVectorStore{}
	p1again, err := New(client, versionedChunker{version: 1}, &fakeEmbedder{}, v1again, PipelineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := p1again.Index(ctx, doc); err != nil {
		t.Fatal(err)
	}
	if len(v1again.upserted) != 0 {
		t.Errorf("same version must skip, but embedded %d chunks", len(v1again.upserted))
	}

	// Same body, bumped chunker version: re-chunked rather than skipped. The
	// stored version is the observable -- a skip would leave it at 1.
	v2 := &fakeVectorStore{}
	p2, err := New(client, versionedChunker{version: 2}, &fakeEmbedder{}, v2, PipelineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := p2.Index(ctx, doc); err != nil {
		t.Fatalf("v2 index: %v", err)
	}
	got, err := client.Document.Get(ctx, doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ChunkerVersion != 2 {
		t.Fatalf("stored chunker_version = %d, want 2: the bump did not re-chunk", got.ChunkerVersion)
	}

	// This test chunker produces identical text at either version, so the
	// per-chunk skip must reuse the existing embeddings: re-chunking is not
	// re-embedding. A real bump changes the text, and those chunks do re-embed.
	if len(v2.upserted) != 0 {
		t.Errorf("re-embedded %d chunks whose text did not change", len(v2.upserted))
	}
}

// TestSkipRechunksWhenURLChanges covers the other input the body hash misses.
// doc.URL is propagated onto every chunk as source_url, and it only moves when a
// source's base URL is reconfigured -- at which point nothing else about the
// document moves, so a body-only check would keep every chunk's source_url
// pointing at the old host forever.
func TestSkipRechunksWhenURLChanges(t *testing.T) {
	client := openTestDB(t)
	defer cleanDB(t, client)

	ctx := context.Background()
	doc := index.Document{
		ID:       uuid.New(),
		Source:   index.SourceGitDocs("test/urlmove"),
		SourceID: "test/urlmove",
		Title:    "Test",
		Body:     "body",
		URL:      "https://old.example.com/a",
	}

	store := &fakeVectorStore{}
	p := newTestPipeline(t, client, store)
	if err := p.Index(ctx, doc); err != nil {
		t.Fatalf("first index: %v", err)
	}

	// Same body, new URL: must re-chunk so chunks carry the new source_url. The
	// chunk text is unchanged, so nothing re-embeds -- persist still rewrites
	// each chunk's metadata, which is where source_url lives.
	store.upserted = nil
	moved := doc
	moved.URL = "https://new.example.com/a"
	if err := p.Index(ctx, moved); err != nil {
		t.Fatalf("second index: %v", err)
	}

	rows, err := client.Chunk.Query().Where(ourChunks("test/urlmove")).All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("expected chunks")
	}
	for _, r := range rows {
		if got := r.Metadata["source_url"]; got != moved.URL {
			t.Errorf("chunk %s: source_url = %v, want %s", r.ID, got, moved.URL)
		}
	}

	// And the row itself records the new URL.
	gotDoc, err := client.Document.Get(ctx, doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotDoc.SourceURL != moved.URL {
		t.Errorf("stored source_url = %q, want %q", gotDoc.SourceURL, moved.URL)
	}
}

func TestSkipWhenNothingChanged(t *testing.T) {
	client := openTestDB(t)
	defer cleanDB(t, client)

	ctx := context.Background()
	doc := index.Document{
		ID:       uuid.New(),
		Source:   index.SourceGitDocs("test/nochange"),
		SourceID: "test/nochange",
		Title:    "Test",
		Body:     "body",
		URL:      "https://example.com/a",
	}
	store := &fakeVectorStore{}
	p := newTestPipeline(t, client, store)
	if err := p.Index(ctx, doc); err != nil {
		t.Fatal(err)
	}

	store.upserted = nil
	if err := p.Index(ctx, doc); err != nil {
		t.Fatal(err)
	}
	if len(store.upserted) != 0 {
		t.Errorf("re-indexing an identical document must skip, embedded %d", len(store.upserted))
	}
}
