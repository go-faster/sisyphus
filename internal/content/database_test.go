package content

import (
	"context"
	stdsql "database/sql"
	"os"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/index"
)

func openTestDB(t *testing.T) *ent.Client {
	t.Helper()
	dsn := os.Getenv("SISYPHUS_TEST_DB")
	if dsn == "" {
		t.Skip("SISYPHUS_TEST_DB not set")
	}

	db, err := stdsql.Open("pgx", dsn)
	require.NoError(t, err, "failed to open database")
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() {
		_ = client.Close()
	})

	ctx := context.Background()
	err = client.Schema.Create(ctx)
	require.NoError(t, err, "failed to create schema")
	return client
}

func TestDatabaseReaderResolveContent(t *testing.T) {
	client := openTestDB(t)
	lg := zaptest.NewLogger(t)
	reader := NewDatabaseReader(client, Options{Logger: lg})

	ctx := context.Background()

	testContent := "line 1\nline 2\nline 3\nline 4\nline 5"

	t.Run("found via git_docs prefix", func(t *testing.T) {
		// Create a document with git_docs prefix
		doc, err := client.Document.Create().
			SetID(index.NewID()).
			SetSource(string(index.SourceGitDocs("myrepo"))).
			SetSourceID("myrepo:test.md").
			SetTitle("Test Document").
			SetBody(testContent).
			Save(ctx)
		require.NoError(t, err)

		resp, err := reader.ResolveContent(ctx, index.ContentRequest{
			Repo: "myrepo",
			Path: "test.md",
		})
		require.NoError(t, err)
		require.True(t, resp.Found)
		require.Equal(t, "database", resp.Source)
		require.Equal(t, testContent, resp.Content)

		// Cleanup
		require.NoError(t, client.Document.DeleteOne(doc).Exec(ctx))
	})

	t.Run("found via git_code prefix when git_docs doesn't match", func(t *testing.T) {
		// Create a document with git_code prefix
		doc, err := client.Document.Create().
			SetID(index.NewID()).
			SetSource(string(index.SourceGitCode("myrepo"))).
			SetSourceID("myrepo:main.go").
			SetTitle("Main File").
			SetBody(testContent).
			Save(ctx)
		require.NoError(t, err)

		resp, err := reader.ResolveContent(ctx, index.ContentRequest{
			Repo: "myrepo",
			Path: "main.go",
		})
		require.NoError(t, err)
		require.True(t, resp.Found)
		require.Equal(t, "database", resp.Source)
		require.Equal(t, testContent, resp.Content)

		// Cleanup
		require.NoError(t, client.Document.DeleteOne(doc).Exec(ctx))
	})

	t.Run("found via git_manifest prefix when git_docs and git_code don't match", func(t *testing.T) {
		// Create a document with git_manifest prefix
		doc, err := client.Document.Create().
			SetID(index.NewID()).
			SetSource(string(index.SourceGitManifest("myrepo"))).
			SetSourceID("myrepo:docker-compose.yml").
			SetTitle("Docker Compose").
			SetBody(testContent).
			Save(ctx)
		require.NoError(t, err)

		resp, err := reader.ResolveContent(ctx, index.ContentRequest{
			Repo: "myrepo",
			Path: "docker-compose.yml",
		})
		require.NoError(t, err)
		require.True(t, resp.Found)
		require.Equal(t, "database", resp.Source)
		require.Equal(t, testContent, resp.Content)

		// Cleanup
		require.NoError(t, client.Document.DeleteOne(doc).Exec(ctx))
	})

	t.Run("not found when no document matches", func(t *testing.T) {
		resp, err := reader.ResolveContent(ctx, index.ContentRequest{
			Repo: "nonexistent",
			Path: "missing.txt",
		})
		require.NoError(t, err)
		require.False(t, resp.Found)
	})

	t.Run("line range slicing", func(t *testing.T) {
		// Create a document
		doc, err := client.Document.Create().
			SetID(index.NewID()).
			SetSource(string(index.SourceGitDocs("myrepo"))).
			SetSourceID("myrepo:lines.txt").
			SetTitle("Lines").
			SetBody(testContent).
			Save(ctx)
		require.NoError(t, err)

		// Test getting lines 2-4
		resp, err := reader.ResolveContent(ctx, index.ContentRequest{
			Repo:  "myrepo",
			Path:  "lines.txt",
			Start: 2,
			End:   4,
		})
		require.NoError(t, err)
		require.True(t, resp.Found)
		require.Equal(t, "line 2\nline 3\nline 4", resp.Content)

		// Cleanup
		require.NoError(t, client.Document.DeleteOne(doc).Exec(ctx))
	})

	t.Run("line range single line", func(t *testing.T) {
		// Create a document
		doc, err := client.Document.Create().
			SetID(index.NewID()).
			SetSource(string(index.SourceGitDocs("myrepo"))).
			SetSourceID("myrepo:single.txt").
			SetTitle("Single").
			SetBody(testContent).
			Save(ctx)
		require.NoError(t, err)

		// Test getting line 3 only
		resp, err := reader.ResolveContent(ctx, index.ContentRequest{
			Repo:  "myrepo",
			Path:  "single.txt",
			Start: 3,
			End:   3,
		})
		require.NoError(t, err)
		require.True(t, resp.Found)
		require.Equal(t, "line 3", resp.Content)

		// Cleanup
		require.NoError(t, client.Document.DeleteOne(doc).Exec(ctx))
	})

	t.Run("line range start beyond end of lines", func(t *testing.T) {
		// Create a document
		doc, err := client.Document.Create().
			SetID(index.NewID()).
			SetSource(string(index.SourceGitDocs("myrepo"))).
			SetSourceID("myrepo:beyond.txt").
			SetTitle("Beyond").
			SetBody(testContent).
			Save(ctx)
		require.NoError(t, err)

		// Test requesting lines beyond the content
		resp, err := reader.ResolveContent(ctx, index.ContentRequest{
			Repo:  "myrepo",
			Path:  "beyond.txt",
			Start: 100,
			End:   200,
		})
		require.NoError(t, err)
		require.True(t, resp.Found)
		require.Equal(t, "", resp.Content)

		// Cleanup
		require.NoError(t, client.Document.DeleteOne(doc).Exec(ctx))
	})

	t.Run("line range start > end adjusts to end", func(t *testing.T) {
		// Create a document
		doc, err := client.Document.Create().
			SetID(index.NewID()).
			SetSource(string(index.SourceGitDocs("myrepo"))).
			SetSourceID("myrepo:reverse.txt").
			SetTitle("Reverse").
			SetBody(testContent).
			Save(ctx)
		require.NoError(t, err)

		// Test when start > end, it should return empty
		resp, err := reader.ResolveContent(ctx, index.ContentRequest{
			Repo:  "myrepo",
			Path:  "reverse.txt",
			Start: 4,
			End:   2,
		})
		require.NoError(t, err)
		require.True(t, resp.Found)
		// When start > end after adjustment, lines[start:end] will be empty
		require.Equal(t, "", resp.Content)

		// Cleanup
		require.NoError(t, client.Document.DeleteOne(doc).Exec(ctx))
	})

	t.Run("line range end zero uses all remaining lines", func(t *testing.T) {
		// Create a document
		doc, err := client.Document.Create().
			SetID(index.NewID()).
			SetSource(string(index.SourceGitDocs("myrepo"))).
			SetSourceID("myrepo:alllines.txt").
			SetTitle("AllLines").
			SetBody(testContent).
			Save(ctx)
		require.NoError(t, err)

		// Test getting from line 3 to end
		resp, err := reader.ResolveContent(ctx, index.ContentRequest{
			Repo:  "myrepo",
			Path:  "alllines.txt",
			Start: 3,
			End:   0,
		})
		require.NoError(t, err)
		require.True(t, resp.Found)
		require.Equal(t, "line 3\nline 4\nline 5", resp.Content)

		// Cleanup
		require.NoError(t, client.Document.DeleteOne(doc).Exec(ctx))
	})

	t.Run("line range start zero treated as start from beginning", func(t *testing.T) {
		// Create a document
		doc, err := client.Document.Create().
			SetID(index.NewID()).
			SetSource(string(index.SourceGitDocs("myrepo"))).
			SetSourceID("myrepo:fromstart.txt").
			SetTitle("FromStart").
			SetBody(testContent).
			Save(ctx)
		require.NoError(t, err)

		// Test getting from beginning to line 2
		resp, err := reader.ResolveContent(ctx, index.ContentRequest{
			Repo:  "myrepo",
			Path:  "fromstart.txt",
			Start: 0,
			End:   2,
		})
		require.NoError(t, err)
		require.True(t, resp.Found)
		require.Equal(t, "line 1\nline 2", resp.Content)

		// Cleanup
		require.NoError(t, client.Document.DeleteOne(doc).Exec(ctx))
	})
}
