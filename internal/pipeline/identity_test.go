package pipeline

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/go-faster/sisyphus/internal/ent/chunk"
	"github.com/go-faster/sisyphus/internal/ent/document"
	"github.com/go-faster/sisyphus/internal/ent/predicate"
	"github.com/go-faster/sisyphus/internal/index"
)

// ourChunks scopes a chunk query to one test's document. The DB-backed suites
// share a database, so an unscoped Chunk.Query() sees other packages' fixtures
// -- vectorrepair's, for instance, are deliberately mismatched, which is exactly
// what these tests assert against.
func ourChunks(sourceID string) predicate.Chunk {
	return chunk.HasDocumentWith(document.SourceID(sourceID))
}

// TestPipeline_ChunkIDMatchesPointIDAfterUnembeddedFirstPass is a regression
// test for chunk rows whose id and qdrant_point_id diverged.
//
// A document indexed while the vector store was unavailable leaves chunk rows
// with qdrant_point_id NULL. When the document later changes, the pipeline
// re-chunks it — and any section that did *not* change still matches an existing
// row on (chunk_index, text_hash). The chunker hands back fresh UUIDs each call,
// so the pipeline used to embed that unchanged section under the new UUID while
// persist's upsert kept the row's original id: the point ended up keyed by an id
// no row holds. Retrieval hydrates a vector hit's text by chunk id, so from then
// on that chunk resolved to empty text on every search.
func TestPipeline_ChunkIDMatchesPointIDAfterUnembeddedFirstPass(t *testing.T) {
	client := openTestDB(t)
	defer cleanDB(t, client)

	ctx := context.Background()
	const stable = "first section"
	doc := index.Document{
		ID:       uuid.New(),
		Source:   index.SourceGitDocs("test/identity"),
		SourceID: "test/identity",
		Title:    "Test",
		Body:     stable + "\n===\nsecond section",
	}

	// First pass with no vector store: rows land with qdrant_point_id NULL,
	// exactly as they do when the vector store is down.
	offline, err := New(client, testChunker{}, &fakeEmbedder{}, nil, PipelineOptions{})
	if err != nil {
		t.Fatalf("new offline pipeline: %v", err)
	}
	if err := offline.Index(ctx, doc); err != nil {
		t.Fatalf("offline index: %v", err)
	}
	rows, err := client.Chunk.Query().Where(chunk.DocumentID(doc.ID)).All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 chunk rows from the offline pass, got %d", len(rows))
	}
	var stableID uuid.UUID
	for _, r := range rows {
		if r.QdrantPointID != nil {
			t.Fatalf("chunk %s: expected no point after an offline pass", r.ID)
		}
		if r.Text == stable {
			stableID = r.ID
		}
	}
	if stableID == uuid.Nil {
		t.Fatal("did not find the stable chunk row")
	}

	// Second pass with the vector store back and the document changed. The body
	// hash moves, so the document is re-chunked, but the first section is byte
	// identical: its row exists and has never been embedded.
	store := &fakeVectorStore{}
	online := newTestPipeline(t, client, store)
	changed := doc
	changed.ID = uuid.New() // ingest hands out a fresh document ID each run
	changed.Body = stable + "\n===\nsecond section, rewritten"
	changed.BodyHash = ""
	if err := online.Index(ctx, changed); err != nil {
		t.Fatalf("online index: %v", err)
	}

	rows, err = client.Chunk.Query().Where(ourChunks("test/identity")).All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var sawStable bool
	for _, r := range rows {
		if r.QdrantPointID == nil {
			t.Errorf("chunk %s: still not embedded after the online pass", r.ID)
			continue
		}
		// The invariant: a chunk's point is keyed by the chunk's own id. Break
		// it and vector search can never hydrate this chunk again.
		if r.ID != *r.QdrantPointID {
			t.Errorf("chunk %s (%q): qdrant_point_id is %s, want it to equal the chunk id",
				r.ID, r.Text, *r.QdrantPointID)
		}
		if r.Text == stable {
			sawStable = true
			if r.ID != stableID {
				t.Errorf("stable chunk changed identity: %s -> %s", stableID, r.ID)
			}
		}
	}
	if !sawStable {
		t.Error("the unchanged section lost its row")
	}

	// Every point written must belong to a row, not to a throwaway UUID.
	live := map[uuid.UUID]bool{}
	for _, r := range rows {
		live[r.ID] = true
	}
	for _, up := range store.upserted {
		if !live[up] {
			t.Errorf("upserted point %s belongs to no chunk row", up)
		}
	}
}

// TestPipeline_StaleRowWithoutPointIsDeleted covers rows the chunker no longer
// produces that were never embedded. Deleting the row used to be gated on it
// having a point, so these survived forever: invisible to vector search but
// still returned by Postgres FTS, which reads every chunk row regardless.
func TestPipeline_StaleRowWithoutPointIsDeleted(t *testing.T) {
	client := openTestDB(t)
	defer cleanDB(t, client)

	ctx := context.Background()
	doc := index.Document{
		ID:       uuid.New(),
		Source:   index.SourceGitDocs("test/stalerow"),
		SourceID: "test/stalerow",
		Title:    "Test",
		Body:     "keep me\n===\ndelete me",
	}

	// Index with no vector store, so both rows land without a point.
	offline, err := New(client, testChunker{}, &fakeEmbedder{}, nil, PipelineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := offline.Index(ctx, doc); err != nil {
		t.Fatalf("offline index: %v", err)
	}

	// Drop the second section. Its row was never embedded, but it is stale all
	// the same.
	doc.Body = "keep me"
	doc.BodyHash = ""
	if err := offline.Index(ctx, doc); err != nil {
		t.Fatalf("second index: %v", err)
	}

	rows, err := client.Chunk.Query().Where(ourChunks("test/stalerow")).All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Text == "delete me" {
			t.Fatalf("stale never-embedded row %s survived; FTS would keep returning it", r.ID)
		}
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row to remain, got %d", len(rows))
	}
}

// TestPipeline_StaleDeleteUsesRecordedPointID covers legacy rows whose id and
// qdrant_point_id already diverged: the cleanup must delete the point the row
// recorded, not the row's own id. Deleting the row id missed the real point and
// stranded it in the vector store with nothing left to find it by.
func TestPipeline_StaleDeleteUsesRecordedPointID(t *testing.T) {
	client := openTestDB(t)
	defer cleanDB(t, client)

	ctx := context.Background()
	store := &fakeVectorStore{}
	p := newTestPipeline(t, client, store)

	doc := index.Document{
		ID:       uuid.New(),
		Source:   index.SourceGitDocs("test/legacy"),
		SourceID: "test/legacy",
		Title:    "Test",
		Body:     "keep me\n===\ndelete me",
	}
	if err := p.Index(ctx, doc); err != nil {
		t.Fatalf("first index: %v", err)
	}

	// Simulate a row written before the id/point invariant was enforced.
	legacyPoint := uuid.New()
	stale, err := client.Chunk.Query().
		Where(ourChunks("test/legacy"), chunk.Text("delete me")).
		Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Chunk.UpdateOneID(stale.ID).SetQdrantPointID(legacyPoint).Save(ctx); err != nil {
		t.Fatal(err)
	}
	store.deleted = nil

	// Drop that section so the row goes stale.
	doc.Body = "keep me"
	doc.BodyHash = ""
	if err := p.Index(ctx, doc); err != nil {
		t.Fatalf("second index: %v", err)
	}

	var sawLegacy bool
	for _, d := range store.deleted {
		if d == legacyPoint {
			sawLegacy = true
		}
		if d == stale.ID {
			t.Errorf("deleted the row id %s; the point actually lives at %s", stale.ID, legacyPoint)
		}
	}
	if !sawLegacy {
		t.Errorf("point %s was never deleted, leaking an orphan; deleted=%v", legacyPoint, store.deleted)
	}
}
