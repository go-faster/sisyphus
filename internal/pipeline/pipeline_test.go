package pipeline

import (
	"context"
	stdsql "database/sql"
	"os"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/ent/chunk"
	"github.com/go-faster/sisyphus/internal/ent/document"
	"github.com/go-faster/sisyphus/internal/index"
)

// fakeVectorStore records Upsert and Delete calls in memory.
type fakeVectorStore struct {
	upserted []uuid.UUID
	deleted  []uuid.UUID
}

func (f *fakeVectorStore) Upsert(_ context.Context, chunks []index.Chunk, _ [][]float32) error {
	for _, c := range chunks {
		f.upserted = append(f.upserted, c.ID)
	}
	return nil
}

func (f *fakeVectorStore) Delete(_ context.Context, ids []uuid.UUID) error {
	f.deleted = append(f.deleted, ids...)
	return nil
}

type fakeEmbedder struct{}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	vectors := make([][]float32, len(texts))
	for i := range texts {
		vectors[i] = make([]float32, 4)
		for j := range vectors[i] {
			vectors[i][j] = float32(len(texts[i]) * (i + 1))
		}
	}
	return vectors, nil
}

func (f *fakeEmbedder) Dim() int { return 4 }

// testChunker splits a Body on "\n===\n" markers. It produces fresh random UUIDs
// each call (like real chunkers) and is deterministic in content.
type testChunker struct{}

func (testChunker) Chunk(_ context.Context, doc index.Document) ([]index.Chunk, error) {
	sections := strings.Split(doc.Body, "\n===\n")
	chunks := make([]index.Chunk, 0, len(sections))
	for i, section := range sections {
		section = strings.TrimSpace(section)
		if section == "" {
			continue
		}
		chunks = append(chunks, index.Chunk{
			ID:         index.NewID(),
			DocumentID: doc.ID,
			Index:      i,
			Type:       index.ChunkSection,
			Title:      doc.Title,
			Text:       section,
			TextHash:   index.Hash(section),
			Metadata:   make(map[string]any),
		})
	}
	return chunks, nil
}

func openTestDB(t *testing.T) *ent.Client {
	t.Helper()
	dsn := os.Getenv("SISYPHUS_TEST_DB")
	if dsn == "" {
		t.Skip("SISYPHUS_TEST_DB not set")
	}

	db, err := stdsql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql open: %v", err)
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() {
		_ = client.Close()
	})

	ctx := context.Background()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return client
}

func cleanDB(t *testing.T, client *ent.Client) {
	t.Helper()
	ctx := context.Background()
	_, _ = client.Chunk.Delete().Exec(ctx)
	_, _ = client.Document.Delete().Exec(ctx)
}

func newTestPipeline(t *testing.T, client *ent.Client, store *fakeVectorStore) *Pipeline {
	t.Helper()
	p, err := New(client, testChunker{}, &fakeEmbedder{}, store, PipelineOptions{})
	if err != nil {
		t.Fatalf("new pipeline: %v", err)
	}
	return p
}

func TestPipeline_UnchangedDoc(t *testing.T) {
	client := openTestDB(t)
	defer cleanDB(t, client)

	store := &fakeVectorStore{}
	p := newTestPipeline(t, client, store)

	ctx := context.Background()
	doc := index.Document{
		ID:       uuid.New(),
		Source:   index.SourceGitDocs("test/unchanged"),
		SourceID: "test/unchanged",
		Title:    "Test",
		Body:     "section one\n===\nsection two",
	}

	if err := p.Index(ctx, doc); err != nil {
		t.Fatalf("first index: %v", err)
	}
	if len(store.upserted) != 2 {
		t.Fatalf("expected 2 upserted, got %d", len(store.upserted))
	}
	if len(store.deleted) != 0 {
		t.Fatalf("expected 0 deleted, got %d", len(store.deleted))
	}

	// Clear recorded calls.
	store.upserted = nil
	store.deleted = nil

	// Second call with same body → should be a no-op.
	if err := p.Index(ctx, doc); err != nil {
		t.Fatalf("second index: %v", err)
	}
	if len(store.upserted) != 0 {
		t.Errorf("expected 0 upserted on unchanged doc, got %d", len(store.upserted))
	}
	if len(store.deleted) != 0 {
		t.Errorf("expected 0 deleted on unchanged doc, got %d", len(store.deleted))
	}
}

func TestPipeline_ChangedDoc(t *testing.T) {
	client := openTestDB(t)
	defer cleanDB(t, client)

	store := &fakeVectorStore{}
	p := newTestPipeline(t, client, store)

	ctx := context.Background()
	doc := index.Document{
		ID:       uuid.New(),
		Source:   index.SourceGitDocs("test/changed"),
		SourceID: "test/changed",
		Title:    "Test",
		Body:     "stable section\n===\nwill change",
	}

	// First index: embed both chunks.
	if err := p.Index(ctx, doc); err != nil {
		t.Fatalf("first index: %v", err)
	}
	if len(store.upserted) != 2 {
		t.Fatalf("first index: expected 2 upserted, got %d", len(store.upserted))
	}
	firstUpserted := make([]uuid.UUID, len(store.upserted))
	copy(firstUpserted, store.upserted)
	store.upserted = nil

	// Second index: change the second section.
	doc.Body = "stable section\n===\nnew content"
	doc.BodyHash = index.Hash(doc.Body)

	if err := p.Index(ctx, doc); err != nil {
		t.Fatalf("second index: %v", err)
	}

	// Only the new/changed chunk should be upserted.
	if len(store.upserted) != 1 {
		t.Fatalf("expected 1 upserted on changed doc, got %d", len(store.upserted))
	}

	// The stale chunk (old "will change") should be deleted.
	if len(store.deleted) != 1 {
		t.Fatalf("expected 1 deleted on changed doc, got %d", len(store.deleted))
	}
	if store.deleted[0] != firstUpserted[1] {
		t.Errorf("deleted ID %v does not match old second chunk %v", store.deleted[0], firstUpserted[1])
	}
}

func TestPipeline_NewDoc(t *testing.T) {
	client := openTestDB(t)
	defer cleanDB(t, client)

	store := &fakeVectorStore{}
	p := newTestPipeline(t, client, store)

	ctx := context.Background()
	doc := index.Document{
		ID:       uuid.New(),
		Source:   index.SourceGitDocs("test/new"),
		SourceID: "test/new",
		Title:    "New Doc",
		Body:     "part a\n===\npart b\n===\npart c",
	}

	if err := p.Index(ctx, doc); err != nil {
		t.Fatalf("index: %v", err)
	}
	if len(store.upserted) != 3 {
		t.Errorf("expected 3 upserted, got %d", len(store.upserted))
	}
	if len(store.deleted) != 0 {
		t.Errorf("expected 0 deleted, got %d", len(store.deleted))
	}
}

func TestPipeline_Idempotent(t *testing.T) {
	client := openTestDB(t)
	defer cleanDB(t, client)

	store := &fakeVectorStore{}
	p := newTestPipeline(t, client, store)

	ctx := context.Background()
	doc := index.Document{
		ID:       uuid.New(),
		Source:   index.SourceGitDocs("test/idempotent"),
		SourceID: "test/idempotent",
		Title:    "Test",
		Body:     "only section",
	}

	if err := p.Index(ctx, doc); err != nil {
		t.Fatalf("first index: %v", err)
	}
	if len(store.upserted) != 1 {
		t.Fatalf("expected 1 upserted, got %d", len(store.upserted))
	}
	store.upserted = nil
	store.deleted = nil

	// Second call.
	if err := p.Index(ctx, doc); err != nil {
		t.Fatalf("second index: %v", err)
	}
	if len(store.upserted) != 0 {
		t.Errorf("expected 0 upserted on repeat, got %d", len(store.upserted))
	}
	if len(store.deleted) != 0 {
		t.Errorf("expected 0 deleted on repeat, got %d", len(store.deleted))
	}

	// Third call.
	if err := p.Index(ctx, doc); err != nil {
		t.Fatalf("third index: %v", err)
	}
	if len(store.upserted) != 0 {
		t.Errorf("expected 0 upserted on second repeat, got %d", len(store.upserted))
	}
	if len(store.deleted) != 0 {
		t.Errorf("expected 0 deleted on second repeat, got %d", len(store.deleted))
	}
}

func TestPipeline_VectorStoreNil(t *testing.T) {
	client := openTestDB(t)
	defer cleanDB(t, client)

	// vectors=nil should not panic.
	p, err := New(client, testChunker{}, &fakeEmbedder{}, nil, PipelineOptions{})
	if err != nil {
		t.Fatalf("new pipeline: %v", err)
	}

	ctx := context.Background()
	doc := index.Document{
		ID:       uuid.New(),
		Source:   index.SourceGitDocs("test/no-vector"),
		SourceID: "test/no-vector",
		Title:    "Test",
		Body:     "some content",
	}

	if err := p.Index(ctx, doc); err != nil {
		t.Fatalf("index: %v", err)
	}
}

func TestPipeline_DocumentIdentityReuse(t *testing.T) {
	client := openTestDB(t)
	defer cleanDB(t, client)

	store := &fakeVectorStore{}
	p := newTestPipeline(t, client, store)

	ctx := context.Background()

	// First ingest: create a document with (source, source_id).
	// Caller provides a fresh random ID each time.
	doc1 := index.Document{
		ID:       uuid.New(),
		Source:   index.SourceGitDocs("test/identity"),
		SourceID: "test/identity",
		Title:    "Test",
		Body:     "old content",
	}

	if err := p.Index(ctx, doc1); err != nil {
		t.Fatalf("first index: %v", err)
	}

	// Verify exactly one document exists with this identity.
	count, err := client.Document.Query().
		Where(
			document.Source(string(doc1.Source)),
			document.SourceID(doc1.SourceID),
		).
		Count(ctx)
	if err != nil {
		t.Fatalf("count documents: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 document with identity, got %d", count)
	}

	// Record the first document's ID from the DB.
	firstDocID, err := client.Document.Query().
		Where(
			document.Source(string(doc1.Source)),
			document.SourceID(doc1.SourceID),
		).
		Only(ctx)
	if err != nil {
		t.Fatalf("get first document: %v", err)
	}

	// Clear recorded vector store calls.
	store.upserted = nil
	store.deleted = nil

	// Second ingest: same identity, different body, with a DIFFERENT doc.ID.
	// This simulates the real ingest flow where each call generates a fresh UUID.
	doc2 := index.Document{
		ID:       uuid.New(), // different from doc1.ID
		Source:   index.SourceGitDocs("test/identity"),
		SourceID: "test/identity",
		Title:    "Test",
		Body:     "new content",
	}

	if err := p.Index(ctx, doc2); err != nil {
		t.Fatalf("second index: %v", err)
	}

	// Verify still exactly one document with this identity.
	count, err = client.Document.Query().
		Where(
			document.Source(string(doc2.Source)),
			document.SourceID(doc2.SourceID),
		).
		Count(ctx)
	if err != nil {
		t.Fatalf("count documents after second index: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 document after second index, got %d", count)
	}

	// Verify the document ID was reused (not a new one).
	secondDocID, err := client.Document.Query().
		Where(
			document.Source(string(doc2.Source)),
			document.SourceID(doc2.SourceID),
		).
		Only(ctx)
	if err != nil {
		t.Fatalf("get second document: %v", err)
	}
	if secondDocID.ID != firstDocID.ID {
		t.Errorf("expected document ID to be reused (%v), but got %v", firstDocID.ID, secondDocID.ID)
	}

	// Both doc1 and doc2 produce exactly one chunk (no "\n===\n" separator in
	// either body), so a raw count can't distinguish "old chunk replaced" from
	// "old chunk left behind": both cases show count == firstChunkCount == 1.
	// Assert on content instead: only the new chunk's text/hash should remain.
	remainingChunks, err := client.Chunk.Query().
		Where(chunk.DocumentID(firstDocID.ID)).
		All(ctx)
	if err != nil {
		t.Fatalf("get remaining chunks: %v", err)
	}
	if len(remainingChunks) != 1 {
		t.Fatalf("expected exactly 1 remaining chunk, got %d", len(remainingChunks))
	}
	if remainingChunks[0].Text != "new content" {
		t.Errorf("expected remaining chunk text %q, got %q", "new content", remainingChunks[0].Text)
	}
	if remainingChunks[0].TextHash == index.Hash("old content") {
		t.Errorf("stale chunk (old content hash) was not deleted")
	}
}

func TestPipeline_StaleChunkDeletion(t *testing.T) {
	client := openTestDB(t)
	defer cleanDB(t, client)

	store := &fakeVectorStore{}
	p := newTestPipeline(t, client, store)

	ctx := context.Background()

	// First ingest: document with multiple chunks.
	doc1 := index.Document{
		ID:       uuid.New(),
		Source:   index.SourceGitDocs("test/stale"),
		SourceID: "test/stale",
		Title:    "Test",
		Body:     "chunk a\n===\nchunk b\n===\nchunk c",
	}

	if err := p.Index(ctx, doc1); err != nil {
		t.Fatalf("first index: %v", err)
	}

	// Verify 3 chunks exist.
	docID, _ := client.Document.Query().
		Where(
			document.Source(string(doc1.Source)),
			document.SourceID(doc1.SourceID),
		).
		Only(ctx)

	chunkCount, err := client.Chunk.Query().
		Where(chunk.DocumentID(docID.ID)).
		Count(ctx)
	if err != nil {
		t.Fatalf("count initial chunks: %v", err)
	}
	if chunkCount != 3 {
		t.Errorf("expected 3 initial chunks, got %d", chunkCount)
	}

	store.deleted = nil

	// Second ingest: same identity, drop "chunk c" entirely and modify "chunk b".
	// testChunker splits on \n===\n, so this produces different (index, text_hash)
	// keys for both "chunk b" (same index, new hash) and "chunk c" (gone) — both
	// of the original chunk b/c rows become stale; only "chunk a" is reused as-is.
	doc2 := index.Document{
		ID:       uuid.New(),
		Source:   index.SourceGitDocs("test/stale"),
		SourceID: "test/stale",
		Title:    "Test",
		Body:     "chunk a\n===\nchunk b modified",
	}

	if err := p.Index(ctx, doc2); err != nil {
		t.Fatalf("second index: %v", err)
	}

	// Verify 2 chunks now exist (one stable, one modified).
	chunkCount, err = client.Chunk.Query().
		Where(chunk.DocumentID(docID.ID)).
		Count(ctx)
	if err != nil {
		t.Fatalf("count final chunks: %v", err)
	}
	if chunkCount != 2 {
		t.Errorf("expected 2 final chunks, got %d", chunkCount)
	}

	// Verify both stale chunks (old "chunk b" and removed "chunk c") were
	// deleted from Postgres (not just vectors).
	if len(store.deleted) != 2 {
		t.Errorf("expected 2 stale chunks deleted from vectors, got %d", len(store.deleted))
	}
}
