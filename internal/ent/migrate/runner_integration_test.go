//go:build integration

package migrate_test

import (
	"database/sql"
	"sync"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	entmigrate "github.com/go-faster/sisyphus/internal/ent/migrate"
)

// TestRunConcurrentCallersSerialize is a regression test for the schema_migrations
// PK race: before Run took an advisory lock, N processes migrating on startup
// (e.g. N ssapi replicas) would race applying the same pending file, and the
// loser's INSERT into schema_migrations hit a primary-key violation.
func TestRunConcurrentCallersSerialize(t *testing.T) {
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

	const callers = 5
	var wg sync.WaitGroup
	errs := make([]error, callers)
	for i := range callers {
		wg.Go(func() {
			errs[i] = entmigrate.NewRunner(db).Run(ctx)
		})
	}
	wg.Wait()

	for i, err := range errs {
		require.NoErrorf(t, err, "caller %d", i)
	}

	pending, err := entmigrate.NewRunner(db).Pending(ctx)
	require.NoError(t, err)
	require.Empty(t, pending)
}
