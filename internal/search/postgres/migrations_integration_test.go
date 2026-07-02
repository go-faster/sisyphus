//go:build integration

package postgres

import (
	"database/sql"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/go-faster/sisyphus/internal/ent"
	entmigrate "github.com/go-faster/sisyphus/internal/ent/migrate"
)

func TestMigrationsE2E(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx := t.Context()
	container, err := tcpostgres.Run(ctx,
		"postgres:17-alpine",
		tcpostgres.WithDatabase("sisyphus"),
		tcpostgres.WithUsername("sisyphus"),
		tcpostgres.WithPassword("sisyphus"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, testcontainers.TerminateContainer(container))
	})

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	r := entmigrate.NewRunner(db)

	require.NoError(t, r.Run(ctx))

	// Verify tracking table has entries.
	var count int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&count))
	require.Equal(t, 2, count)

	// Verify tables exist.
	tables := []string{"documents", "chunks", "support_requests", "sync_states", "telegram_messages"}
	for _, name := range tables {
		var exists bool
		require.NoError(t, db.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name = $1)`, name,
		).Scan(&exists))
		require.True(t, exists, "table %s should exist", name)
	}

	// Verify FTS column and index.
	var hasFTS bool
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT FROM information_schema.columns WHERE table_name='chunks' AND column_name='search_vector')`,
	).Scan(&hasFTS))
	require.True(t, hasFTS, "search_vector column should exist")

	// Verify ent can query.
	n, err := client.Document.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, n)

	// Idempotency.
	require.NoError(t, r.Run(ctx))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&count))
	require.Equal(t, 2, count)

	// postgres searcher still works.
	searcher := New(db, client)
	require.NoError(t, searcher.Migrate(ctx), "fts migrate idempotency")
}
