package pglock

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/go-faster/errors"
	"github.com/stretchr/testify/require"

	_ "github.com/jackc/pgx/v5/stdlib" // register pgx driver
)

func openLockTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("SISYPHUS_TEST_DB")
	if dsn == "" {
		t.Skip("SISYPHUS_TEST_DB not set")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestWithExcludes is the whole point: a second run on the same key is skipped
// rather than interleaved with the first.
func TestWithExcludes(t *testing.T) {
	db := openLockTestDB(t)
	ctx := context.Background()

	const key = "test-pglock/exclusive"

	inner := 0
	err := With(ctx, db, key, func(ctx context.Context) error {
		// Re-entering while the lock is held must be refused. pg_try_advisory_lock
		// is per-session and this call takes a different pooled connection, so
		// this is the same situation as a second process.
		return With(ctx, db, key, func(context.Context) error {
			inner++
			return nil
		})
	})
	require.ErrorIs(t, err, ErrHeld)
	require.Zero(t, inner, "the contended run must not have executed")
}

// TestWithReleases pins that the lock does not outlive the run, including when
// the run fails.
func TestWithReleases(t *testing.T) {
	db := openLockTestDB(t)
	ctx := context.Background()

	const key = "test-pglock/released"

	boom := errors.New("run failed")
	require.ErrorIs(t, With(ctx, db, key, func(context.Context) error {
		return boom
	}), boom)

	ran := false
	require.NoError(t, With(ctx, db, key, func(context.Context) error {
		ran = true
		return nil
	}))
	require.True(t, ran, "the lock must be released after a failed run")
}

// TestWithDistinctKeys pins that two different keys do not block each other —
// the lock is per key, not global.
func TestWithDistinctKeys(t *testing.T) {
	db := openLockTestDB(t)
	ctx := context.Background()

	ran := false
	require.NoError(t, With(ctx, db, "test-pglock/a", func(ctx context.Context) error {
		return With(ctx, db, "test-pglock/b", func(context.Context) error {
			ran = true
			return nil
		})
	}))
	require.True(t, ran)
}

// TestWithNilDB pins the fallback: no pooled handle means run unlocked rather
// than fail.
func TestWithNilDB(t *testing.T) {
	ran := false
	require.NoError(t, With(context.Background(), nil, "k", func(context.Context) error {
		ran = true
		return nil
	}))
	require.True(t, ran)
}

func TestLockIDIsStable(t *testing.T) {
	require.Equal(t, lockID("git"), lockID("git"))
	require.NotEqual(t, lockID("git"), lockID("jira"))
}
