package vectorrepair

import (
	"context"
	stdsql "database/sql"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/go-faster/errors"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/ent/chunk"
	"github.com/go-faster/sisyphus/internal/ent/document"
	"github.com/go-faster/sisyphus/internal/index"
)

type fakeEmbedder struct {
	err error
}

func (f *fakeEmbedder) Dim() int { return 4 }

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{float32(len(texts[i])), 1, 2, 3}
	}
	return out, nil
}

type fakeVectors struct {
	upserted  []uuid.UUID
	deleted   []uuid.UUID
	deleteErr error
}

func (f *fakeVectors) Upsert(_ context.Context, chunks []index.Chunk, _ [][]float32) error {
	for _, c := range chunks {
		f.upserted = append(f.upserted, c.ID)
	}
	return nil
}

func (f *fakeVectors) Delete(_ context.Context, ids []uuid.UUID) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, ids...)
	return nil
}

// Repairer.Run rewrites every mismatched chunk in the database, so this suite
// cannot share one with another package's fixtures: it would rebind their rows
// (pipeline seeds deliberately-diverged ones) and they would delete its. Give it
// a database of its own, created once for the package, so it is hermetic under
// any -p.
var testDSN string

func TestMain(m *testing.M) {
	dsn := os.Getenv("SISYPHUS_TEST_DB")
	if dsn == "" {
		os.Exit(m.Run()) // every test skips
	}
	name := "vectorrepair_test_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	admin, err := stdsql.Open("pgx", dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vectorrepair: open admin db: %v\n", err)
		os.Exit(1)
	}
	// The name is generated above, never user input, so interpolating it is safe;
	// CREATE DATABASE takes no placeholders anyway.
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		fmt.Fprintf(os.Stderr, "vectorrepair: create %s: %v\n", name, err)
		os.Exit(1)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vectorrepair: parse dsn: %v\n", err)
		os.Exit(1)
	}
	u.Path = "/" + name
	testDSN = u.String()

	code := m.Run()

	if _, err := admin.Exec("DROP DATABASE IF EXISTS " + name); err != nil {
		fmt.Fprintf(os.Stderr, "vectorrepair: drop %s: %v\n", name, err)
	}
	_ = admin.Close()
	os.Exit(code)
}

func openTestDB(t *testing.T) *ent.Client {
	t.Helper()
	if testDSN == "" {
		t.Skip("set SISYPHUS_TEST_DB to run DB tests")
	}
	db, err := stdsql.Open("pgx", testDSN)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Schema.Create(t.Context()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Own database, so a blanket wipe between tests is safe here.
	cleanup := func() {
		ctx := context.Background()
		_, _ = client.Chunk.Delete().Exec(ctx)
		_, _ = client.Document.Delete().Exec(ctx)
	}
	cleanup()
	t.Cleanup(cleanup)
	return client
}

// testSource scopes this suite's fixtures. It must not collide with another
// package's: pipeline's documents are git_docs:test/..., and both suites would
// otherwise match a shared "test/" source-id prefix.
const testSource = "vectorrepair_test"

// seed writes a document with one chunk whose point binding is pointID. Passing
// uuid.Nil binds the chunk to its own ID, i.e. a healthy row.
func seed(t *testing.T, client *ent.Client, text string, pointID uuid.UUID) *ent.Chunk {
	t.Helper()
	ctx := context.Background()
	doc, err := client.Document.Create().
		SetID(uuid.New()).
		SetSource(testSource).
		SetSourceID(testSource + "/" + text).
		SetTitle("t").
		SetBody(text).
		SetBodyHash(index.Hash(text)).
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	if pointID == uuid.Nil {
		pointID = id
	}
	c, err := client.Chunk.Create().
		SetID(id).
		SetDocumentID(doc.ID).
		SetChunkIndex(0).
		SetChunkType(string(index.ChunkSection)).
		SetTitle("t").
		SetText(text).
		SetTextHash(index.Hash(text)).
		SetQdrantPointID(pointID).
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRepairRebindsMismatchedChunk(t *testing.T) {
	client := openTestDB(t)
	ctx := context.Background()

	strayPoint := uuid.New()
	broken := seed(t, client, "broken", strayPoint)
	healthy := seed(t, client, "healthy", uuid.Nil)

	vectors := &fakeVectors{}
	r, err := New(client, &fakeEmbedder{}, vectors, Options{})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := r.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Counts are table-wide (Run repairs everything), so assert only that this
	// suite's row was among what it found and fixed.
	if rep.Mismatched < 1 || rep.Repaired < 1 {
		t.Fatalf("report = %+v, want at least 1 mismatched and repaired", rep)
	}

	// The broken chunk is now bound to a point keyed by its own ID, so a vector
	// hit can hydrate it again.
	got, err := client.Chunk.Get(ctx, broken.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.QdrantPointID == nil || *got.QdrantPointID != broken.ID {
		t.Fatalf("qdrant_point_id = %v, want the chunk's own id %s", got.QdrantPointID, broken.ID)
	}
	if !slices.Contains(vectors.upserted, broken.ID) {
		t.Fatalf("upserted = %v, want it to include the broken chunk's own id %s",
			vectors.upserted, broken.ID)
	}
	if !slices.Contains(vectors.deleted, strayPoint) {
		t.Fatalf("deleted = %v, want it to include the superseded point %s",
			vectors.deleted, strayPoint)
	}

	// The healthy chunk must not be touched.
	stillHealthy, err := client.Chunk.Get(ctx, healthy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if *stillHealthy.QdrantPointID != healthy.ID {
		t.Error("healthy chunk was rebound")
	}
}

func TestRepairDryRunWritesNothing(t *testing.T) {
	client := openTestDB(t)
	ctx := context.Background()

	stray := uuid.New()
	broken := seed(t, client, "broken", stray)

	vectors := &fakeVectors{}
	r, err := New(client, &fakeEmbedder{}, vectors, Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := r.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Mismatched < 1 {
		t.Fatalf("mismatched = %d, want at least 1: a dry run still counts", rep.Mismatched)
	}
	if rep.Repaired != 0 || len(vectors.upserted) != 0 || len(vectors.deleted) != 0 {
		t.Fatalf("dry run wrote: repaired=%d upserted=%v deleted=%v",
			rep.Repaired, vectors.upserted, vectors.deleted)
	}
	got, err := client.Chunk.Get(ctx, broken.ID)
	if err != nil {
		t.Fatal(err)
	}
	if *got.QdrantPointID != stray {
		t.Error("dry run modified the row")
	}
}

// TestRepairLeavesHealthyChunkAlone: a chunk already bound to its own point must
// never be touched.
func TestRepairLeavesHealthyChunkAlone(t *testing.T) {
	client := openTestDB(t)
	healthy := seed(t, client, "healthy", uuid.Nil)

	vectors := &fakeVectors{}
	r, err := New(client, &fakeEmbedder{}, vectors, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(vectors.upserted, healthy.ID) {
		t.Errorf("re-embedded a healthy chunk: upserted = %v", vectors.upserted)
	}
	got, err := client.Chunk.Get(context.Background(), healthy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if *got.QdrantPointID != healthy.ID {
		t.Error("healthy chunk was rebound")
	}
}

// TestRepairLeavesRowIntactWhenEmbedFails pins the write ordering: the row must
// not be rebound to a point that was never written.
func TestRepairLeavesRowIntactWhenEmbedFails(t *testing.T) {
	client := openTestDB(t)
	ctx := context.Background()

	stray := uuid.New()
	broken := seed(t, client, "broken", stray)

	vectors := &fakeVectors{}
	r, err := New(client, &fakeEmbedder{err: errors.New("ollama down")}, vectors, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(ctx); err == nil {
		t.Fatal("expected the embed failure to surface")
	}
	got, err := client.Chunk.Get(ctx, broken.ID)
	if err != nil {
		t.Fatal(err)
	}
	if *got.QdrantPointID != stray {
		t.Error("row was rebound despite the embed failing")
	}
	if len(vectors.deleted) != 0 {
		t.Errorf("deleted %v despite the embed failing", vectors.deleted)
	}
}

// TestRepairSurvivesDeleteFailure covers the tail of the ordering: once the row
// is correct, failing to drop the superseded point is not worth failing the run
// — it is exactly what gc reclaims.
func TestRepairSurvivesDeleteFailure(t *testing.T) {
	client := openTestDB(t)
	ctx := context.Background()

	broken := seed(t, client, "broken", uuid.New())
	vectors := &fakeVectors{deleteErr: errors.New("qdrant down")}
	r, err := New(client, &fakeEmbedder{}, vectors, Options{})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := r.Run(ctx)
	if err != nil {
		t.Fatalf("delete failure should not fail the run: %v", err)
	}
	if rep.Repaired != 1 {
		t.Fatalf("repaired = %d, want 1", rep.Repaired)
	}
	got, err := client.Chunk.Get(ctx, broken.ID)
	if err != nil {
		t.Fatal(err)
	}
	if *got.QdrantPointID != broken.ID {
		t.Error("row should be correct even though the cleanup failed")
	}
}

func TestRepairBatchesUntilDrained(t *testing.T) {
	client := openTestDB(t)
	for i := range 5 {
		seed(t, client, string(rune('a'+i)), uuid.New())
	}
	vectors := &fakeVectors{}
	r, err := New(client, &fakeEmbedder{}, vectors, Options{Batch: 2})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Repaired < 5 {
		t.Fatalf("report = %+v, want at least this suite's 5 repaired across batches", rep)
	}
	// Scoped to this suite: the table is shared, so a table-wide count is not
	// ours to assert.
	n, err := client.Chunk.Query().
		Where(mismatched(), chunk.HasDocumentWith(document.Source(testSource))).
		Count(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d of this suite's chunks still mismatched after the run", n)
	}
}

func TestNewRequiresDeps(t *testing.T) {
	if _, err := New(nil, &fakeEmbedder{}, &fakeVectors{}, Options{}); err == nil {
		t.Error("expected error without db")
	}
	if _, err := New(&ent.Client{}, nil, &fakeVectors{}, Options{}); err == nil {
		t.Error("expected error without embedder")
	}
	if _, err := New(&ent.Client{}, &fakeEmbedder{}, nil, Options{}); err == nil {
		t.Error("expected error without vector store")
	}
}
