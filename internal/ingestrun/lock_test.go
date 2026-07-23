package ingestrun

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

// TestWithSourceLockExcludes is the whole point: a second run on the same
// source is skipped rather than interleaved with the first.
func TestWithSourceLockExcludes(t *testing.T) {
	db := openLockTestDB(t)
	ctx := context.Background()

	const key = "test-ingestrun/exclusive"

	inner := 0
	err := WithSourceLock(ctx, db, key, func(ctx context.Context) error {
		// Re-entering while the lock is held must be refused. pg_try_advisory_lock
		// is per-session and this call takes a different pooled connection, so
		// this is the same situation as a second process.
		return WithSourceLock(ctx, db, key, func(context.Context) error {
			inner++
			return nil
		})
	})
	require.ErrorIs(t, err, ErrLocked)
	require.Zero(t, inner, "the contended run must not have executed")
}

// TestWithSourceLockReleases pins that the lock does not outlive the run,
// including when the run fails.
func TestWithSourceLockReleases(t *testing.T) {
	db := openLockTestDB(t)
	ctx := context.Background()

	const key = "test-ingestrun/released"

	boom := errors.New("run failed")
	require.ErrorIs(t, WithSourceLock(ctx, db, key, func(context.Context) error {
		return boom
	}), boom)

	ran := false
	require.NoError(t, WithSourceLock(ctx, db, key, func(context.Context) error {
		ran = true
		return nil
	}))
	require.True(t, ran, "the lock must be released after a failed run")
}

// TestWithSourceLockDistinctSources pins that two different sources do not
// block each other — the lock is per source, not global.
func TestWithSourceLockDistinctSources(t *testing.T) {
	db := openLockTestDB(t)
	ctx := context.Background()

	ran := false
	require.NoError(t, WithSourceLock(ctx, db, "test-ingestrun/a", func(ctx context.Context) error {
		return WithSourceLock(ctx, db, "test-ingestrun/b", func(context.Context) error {
			ran = true
			return nil
		})
	}))
	require.True(t, ran)
}

// TestWithSourceLockNilDB pins the fallback: no pooled handle means run
// unlocked rather than fail.
func TestWithSourceLockNilDB(t *testing.T) {
	ran := false
	require.NoError(t, WithSourceLock(context.Background(), nil, "k", func(context.Context) error {
		ran = true
		return nil
	}))
	require.True(t, ran)
}

func TestSourceLockIDIsStable(t *testing.T) {
	require.Equal(t, sourceLockID("git"), sourceLockID("git"))
	require.NotEqual(t, sourceLockID("git"), sourceLockID("jira"))
}
