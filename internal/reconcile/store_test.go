package reconcile

import (
	"context"
	stdsql "database/sql"
	"os"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/ent/chunk"
	"github.com/go-faster/sisyphus/internal/ent/document"
	"github.com/go-faster/sisyphus/internal/index"
)

// testSource keeps this suite's fixtures out of every other suite's way: the
// DB-backed tests share one database and run concurrently.
const testSource = index.Source("test_reconcile")

func openTestDB(t *testing.T) *ent.Client {
	t.Helper()
	dsn := os.Getenv("SISYPHUS_TEST_DB")
	if dsn == "" {
		t.Skip("SISYPHUS_TEST_DB not set")
	}

	db, err := stdsql.Open("pgx", dsn)
	require.NoError(t, err)
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.Schema.Create(context.Background()))

	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = client.Chunk.Delete().
			Where(chunk.HasDocumentWith(document.Source(string(testSource)))).
			Exec(ctx)
		_, _ = client.Document.Delete().Where(document.Source(string(testSource))).Exec(ctx)
	})
	return client
}

func writeDoc(t *testing.T, client *ent.Client, sourceID string, chunks int) {
	t.Helper()
	ctx := t.Context()

	doc, err := client.Document.Create().
		SetSource(string(testSource)).
		SetSourceID(sourceID).
		SetTitle(sourceID).
		SetBody("body of " + sourceID).
		SetBodyHash(sourceID).
		Save(ctx)
	require.NoError(t, err)

	for i := range chunks {
		_, err := client.Chunk.Create().
			SetID(uuid.New()).
			SetDocumentID(doc.ID).
			SetText("chunk").
			SetTextHash("hash").
			SetChunkType("body").
			SetChunkIndex(i).
			Save(ctx)
		require.NoError(t, err)
	}
}

func TestEntStoreIndexedSourceIDsRespectsPrefix(t *testing.T) {
	client := openTestDB(t)
	writeDoc(t, client, "grp/one/issues/1", 0)
	writeDoc(t, client, "grp/one/issues/2", 0)
	writeDoc(t, client, "grp/two/issues/1", 0)

	s := NewEntStore(client, nil)

	got, err := s.IndexedSourceIDs(t.Context(), testSource, "grp/one/issues/")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"grp/one/issues/1", "grp/one/issues/2"}, got)

	all, err := s.IndexedSourceIDs(t.Context(), testSource, "")
	require.NoError(t, err)
	require.Len(t, all, 3, "an empty prefix means the whole source")
}

// TestEntStoreDeleteRemovesChunksToo pins that a deleted document takes its
// chunks with it — a chunk whose document is gone is unreachable text that
// vector search would still return.
func TestEntStoreDeleteRemovesChunksToo(t *testing.T) {
	client := openTestDB(t)
	writeDoc(t, client, "grp/one/issues/1", 3)
	writeDoc(t, client, "grp/one/issues/2", 2)

	vectors := &fakeVectors{}
	s := NewEntStore(client, vectors)

	chunks, err := s.DeleteDocuments(t.Context(), testSource, []string{"grp/one/issues/1"})
	require.NoError(t, err)
	require.Equal(t, 3, chunks)
	require.Len(t, vectors.deleted, 3, "the chunks' vector points are dropped too")

	left, err := s.IndexedSourceIDs(t.Context(), testSource, "")
	require.NoError(t, err)
	require.Equal(t, []string{"grp/one/issues/2"}, left)

	remaining, err := client.Chunk.Query().Count(t.Context())
	require.NoError(t, err)
	require.GreaterOrEqual(t, remaining, 2)
}

// TestEntStoreDeleteIsScopedToTheGivenIDs pins that deletion touches exactly
// the ids handed to it, never a prefix or a whole source.
func TestEntStoreDeleteIsScopedToTheGivenIDs(t *testing.T) {
	client := openTestDB(t)
	writeDoc(t, client, "grp/one/issues/1", 1)
	writeDoc(t, client, "grp/one/issues/2", 1)
	writeDoc(t, client, "grp/two/issues/1", 1)

	s := NewEntStore(client, nil)
	_, err := s.DeleteDocuments(t.Context(), testSource, []string{"grp/one/issues/2"})
	require.NoError(t, err)

	left, err := s.IndexedSourceIDs(t.Context(), testSource, "")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"grp/one/issues/1", "grp/two/issues/1"}, left)
}

func TestEntStoreDeleteNothingIsNoOp(t *testing.T) {
	client := openTestDB(t)
	writeDoc(t, client, "grp/one/issues/1", 1)

	s := NewEntStore(client, nil)
	n, err := s.DeleteDocuments(t.Context(), testSource, nil)
	require.NoError(t, err)
	require.Zero(t, n)

	left, err := s.IndexedSourceIDs(t.Context(), testSource, "")
	require.NoError(t, err)
	require.Len(t, left, 1)
}

type fakeVectors struct{ deleted []uuid.UUID }

func (f *fakeVectors) Delete(_ context.Context, ids []uuid.UUID) error {
	f.deleted = append(f.deleted, ids...)
	return nil
}
