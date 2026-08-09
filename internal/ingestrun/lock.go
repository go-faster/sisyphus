package ingestrun

import (
	"context"
	"database/sql"

	"github.com/go-faster/sisyphus/internal/pglock"
)

// ErrLocked reports that another process holds the source's ingestion lock.
var ErrLocked = pglock.ErrHeld

// WithSourceLock runs fn while holding the advisory lock for a source, skipping
// the run entirely if another process already holds it.
//
// It guards the cursor, not the indexing. Indexing is idempotent on
// (source, source_id) and safe to run N-wide, but a cursor is a single value
// that two concurrent runs would interleave writes to: the slower run finishes
// last and rewinds the cursor to where it started, so the window between them
// is re-fetched forever and the source never advances. That is why the lock
// wraps the fetch-and-advance half and nothing else.
//
// The key is passed to pglock unprefixed, so a source's lock ID is the same one
// every released version has taken. Do not decorate it: a rolling upgrade in
// which two versions hash the same source differently is two concurrent runs on
// one cursor, which is the exact failure this prevents.
func WithSourceLock(ctx context.Context, db *sql.DB, key string, fn func(context.Context) error) error {
	return pglock.With(ctx, db, key, fn)
}
