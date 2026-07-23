package indexjob

import (
	"context"
	stdsql "database/sql"
	"os"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/ent/chunk"
	"github.com/go-faster/sisyphus/internal/ent/document"
	"github.com/go-faster/sisyphus/internal/index"
	"github.com/go-faster/sisyphus/internal/queue"

	_ "github.com/jackc/pgx/v5/stdlib" // register pgx driver
)

// testSourcePrefix scopes this suite's fixtures. The DB-backed suites share one
// database, so cleanup must delete only its own rows.
const testSourcePrefix = "test-indexjob/"

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
	return client
}

func cleanDB(t *testing.T, client *ent.Client) {
	t.Helper()
	ctx := context.Background()
	ours := document.SourceIDHasPrefix(testSourcePrefix)
	_, _ = client.Chunk.Delete().Where(chunk.HasDocumentWith(ours)).Exec(ctx)
	_, _ = client.Document.Delete().Where(ours).Exec(ctx)
	// No other package publishes to this queue, so clearing it whole is safe
	// and keeps a failed run from leaking jobs into the next one.
	_, _ = client.ExecContext(ctx, `DELETE FROM queue_jobs WHERE queue = $1`, QueueName)
}

func testDoc(id, body string) index.Document {
	return index.Document{
		ID:       uuid.New(),
		Source:   index.Source(testSourcePrefix + "docs"),
		SourceID: testSourcePrefix + id,
		URL:      "https://example.com/" + id,
		Title:    id,
		Body:     body,
	}
}

func newTestQueue(client *ent.Client) *queue.Postgres {
	return queue.NewPostgres(client, QueueName, queue.PostgresOptions{Owner: "test"})
}

// TestPublisherSkipsUnchanged is the reason the publisher does a lookup at all:
// a poll tick re-walks the whole corpus, and enqueuing all of it every interval
// would make queue volume track corpus size rather than change.
func TestPublisherSkipsUnchanged(t *testing.T) {
	client := openTestDB(t)
	cleanDB(t, client)
	t.Cleanup(func() { cleanDB(t, client) })

	ctx := context.Background()
	q := newTestQueue(client)
	pub, err := NewPublisher(q, KindMarkdown, client, PublisherOptions{})
	require.NoError(t, err)

	doc := testDoc("readme.md", "# Title\n\nprose\n")

	// Never indexed: enqueued.
	require.NoError(t, pub.Index(ctx, doc))
	require.Equal(t, 1, countJobs(t, client))

	// Index it for real, then re-publish the identical document.
	drain(ctx, t, client, q)
	require.NoError(t, pub.Index(ctx, doc))
	require.Zero(t, countJobs(t, client), "an unchanged document must not be enqueued")

	// Body moved: enqueued again.
	changed := doc
	changed.Body = "# Title\n\ndifferent prose\n"
	require.NoError(t, pub.Index(ctx, changed))
	require.Equal(t, 1, countJobs(t, client))
}

// TestPublisherDoesNotDedupByContent pins that a revert is still indexed.
//
// Keying the queue by (source, source_id, body_hash) would look right and be
// wrong: queue dedup covers a job's whole lifetime, so a document edited A->B
// and reverted to A would find its original key spent and never be re-indexed.
func TestPublisherDoesNotDedupByContent(t *testing.T) {
	client := openTestDB(t)
	cleanDB(t, client)
	t.Cleanup(func() { cleanDB(t, client) })

	ctx := context.Background()
	q := newTestQueue(client)
	pub, err := NewPublisher(q, KindMarkdown, client, PublisherOptions{})
	require.NoError(t, err)

	versionA := testDoc("page.md", "# A\n\noriginal\n")
	versionB := versionA
	versionB.Body = "# A\n\nedited\n"

	require.NoError(t, pub.Index(ctx, versionA))
	drain(ctx, t, client, q)
	require.NoError(t, pub.Index(ctx, versionB))
	drain(ctx, t, client, q)

	// Back to the original content.
	require.NoError(t, pub.Index(ctx, versionA))
	require.Equal(t, 1, countJobs(t, client), "a revert must be enqueued, not swallowed by a spent dedup key")

	drain(ctx, t, client, q)
	stored, err := client.Document.Query().
		Where(document.SourceID(versionA.SourceID)).
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, index.Hash(versionA.Body), stored.BodyHash)
}

// TestPublishToWorkerIndexes walks the whole path a document takes: published
// by the fetching process, claimed by a worker, indexed into Postgres.
func TestPublishToWorkerIndexes(t *testing.T) {
	client := openTestDB(t)
	cleanDB(t, client)
	t.Cleanup(func() { cleanDB(t, client) })

	ctx := context.Background()
	q := newTestQueue(client)
	pub, err := NewPublisher(q, KindMarkdown, client, PublisherOptions{})
	require.NoError(t, err)

	doc := testDoc("guide.md", "# Guide\n\nStep one.\n\n## Detail\n\nStep two.\n")
	require.NoError(t, pub.Index(ctx, doc))

	drain(ctx, t, client, q)

	stored, err := client.Document.Query().
		Where(document.SourceID(doc.SourceID)).
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, doc.Title, stored.Title)
	require.Equal(t, doc.URL, stored.SourceURL)

	chunks, err := client.Chunk.Query().Where(chunk.DocumentID(stored.ID)).All(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, chunks, "the worker must have chunked the document")
}

// TestHandlerRejectsUnknownKind pins that an unparseable or unroutable job
// fails loudly rather than being acknowledged as done.
func TestHandlerRejectsUnknownKind(t *testing.T) {
	client := openTestDB(t)
	t.Cleanup(func() { cleanDB(t, client) })

	h, err := NewHandler(client, nil, nil, HandlerOptions{})
	require.NoError(t, err)

	raw, err := Encode(Kind("nope"), testDoc("x.md", "body"))
	require.NoError(t, err)
	require.Error(t, h.Handle(context.Background(), queue.Delivery{Payload: raw}))

	require.Error(t, h.Handle(context.Background(), queue.Delivery{Payload: []byte("{")}))
}

func countJobs(t *testing.T, client *ent.Client) int {
	t.Helper()
	rows, err := client.QueryContext(context.Background(),
		`SELECT count(*) FROM queue_jobs WHERE queue = $1 AND status IN ('pending', 'running')`, QueueName)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	require.True(t, rows.Next())
	var n int
	require.NoError(t, rows.Scan(&n))
	return n
}

// drain claims and runs every outstanding job, exactly as a worker would.
// Embedding is skipped (nil embedder and vector store), so this exercises
// chunking and persistence without needing Ollama or Qdrant.
func drain(ctx context.Context, t *testing.T, client *ent.Client, q *queue.Postgres) {
	t.Helper()
	h, err := NewHandler(client, nil, nil, HandlerOptions{})
	require.NoError(t, err)

	for {
		batch, err := q.Fetch(ctx, 16)
		require.NoError(t, err)
		if len(batch) == 0 {
			return
		}
		for _, d := range batch {
			require.NoError(t, h.Handle(ctx, d))
			require.NoError(t, q.Ack(ctx, d.ID))
		}
	}
}
