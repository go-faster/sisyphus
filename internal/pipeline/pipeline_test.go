package pipeline

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"go.uber.org/zap"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/go-faster/scpbot/internal/ent"
	"github.com/go-faster/scpbot/internal/index"
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
	dsn := os.Getenv("SCPBOT_TEST_DB")
	if dsn == "" {
		t.Skip("SCPBOT_TEST_DB not set")
	}

	client, err := ent.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("ent open: %v", err)
	}
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

func TestPipeline_UnchangedDoc(t *testing.T) {
	client := openTestDB(t)
	defer cleanDB(t, client)

	store := &fakeVectorStore{}
	emb := &fakeEmbedder{}
	p := New(client, testChunker{}, emb, store, zap.NewNop())

	ctx := context.Background()
	doc := index.Document{
		ID:       uuid.New(),
		Source:   index.SourceGitLabDocs,
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
	emb := &fakeEmbedder{}
	p := New(client, testChunker{}, emb, store, zap.NewNop())

	ctx := context.Background()
	doc := index.Document{
		ID:       uuid.New(),
		Source:   index.SourceGitLabDocs,
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
	emb := &fakeEmbedder{}
	p := New(client, testChunker{}, emb, store, zap.NewNop())

	ctx := context.Background()
	doc := index.Document{
		ID:       uuid.New(),
		Source:   index.SourceGitLabDocs,
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
	emb := &fakeEmbedder{}
	p := New(client, testChunker{}, emb, store, zap.NewNop())

	ctx := context.Background()
	doc := index.Document{
		ID:       uuid.New(),
		Source:   index.SourceGitLabDocs,
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
	p := New(client, testChunker{}, &fakeEmbedder{}, nil, zap.NewNop())

	ctx := context.Background()
	doc := index.Document{
		ID:       uuid.New(),
		Source:   index.SourceGitLabDocs,
		SourceID: "test/no-vector",
		Title:    "Test",
		Body:     "some content",
	}

	if err := p.Index(ctx, doc); err != nil {
		t.Fatalf("index: %v", err)
	}
}
