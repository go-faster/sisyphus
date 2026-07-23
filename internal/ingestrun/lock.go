package ingestrun

import (
	"context"
	"database/sql"
	"hash/crc32"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"go.uber.org/zap"
)

// lockNamespace is the classid half of the advisory lock key, keeping source
// locks in their own space. The int64 form used by the schema-migration runner
// (internal/ent/migrate) occupies a different space again, so the two cannot
// collide however the hashes fall.
const lockNamespace = 0x5353494e // "SSIN": SiSyphus INgest

// ErrLocked reports that another process holds the source's ingestion lock.
var ErrLocked = errors.New("source is being ingested elsewhere")

// sourceLockID hashes a source key into the objid half of the lock.
//
// A collision between two sources costs mutual exclusion between two runs that
// did not need it — a delayed poll tick, not incorrect data — so a 32-bit hash
// is the right trade against carrying a hand-maintained per-source id table.
func sourceLockID(key string) int32 {
	return int32(crc32.ChecksumIEEE([]byte(key))) //nolint:gosec // deliberate truncation, see above
}

// WithSourceLock runs fn while holding a Postgres advisory lock for key,
// skipping the run entirely if another process already holds it.
//
// It guards the cursor, not the indexing. Indexing is idempotent on
// (source, source_id) and safe to run N-wide, but a cursor is a single value
// that two concurrent runs would interleave writes to: the slower run finishes
// last and rewinds the cursor to where it started, so the window between them
// is re-fetched forever and the source never advances. That is why the lock
// wraps the fetch-and-advance half and nothing else.
//
// The lock is session-scoped and held on a dedicated connection, so it spans
// the whole run rather than a transaction — a run is minutes of HTTP fetching
// and must not sit inside an open transaction for its duration. A process that
// dies drops its connection, and Postgres releases the lock with it.
func WithSourceLock(ctx context.Context, db *sql.DB, key string, fn func(context.Context) error) error {
	if db == nil {
		// No pooled handle available (some one-shot paths build only an ent
		// client). Running unlocked matches the behavior before locking
		// existed rather than failing the run.
		return fn(ctx)
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return errors.Wrap(err, "acquire connection for source lock")
	}
	defer func() { _ = conn.Close() }()

	id := sourceLockID(key)
	var acquired bool
	if err := conn.QueryRowContext(ctx,
		`SELECT pg_try_advisory_lock($1, $2)`, int32(lockNamespace), id,
	).Scan(&acquired); err != nil {
		return errors.Wrap(err, "try source advisory lock")
	}
	if !acquired {
		zctx.From(ctx).Info("skipping run, source locked by another process",
			zap.String("source", key))
		return ErrLocked
	}
	defer func() {
		// Unlock on the same connection, and without the run's context: a run
		// canceled by shutdown must still release, or the lock waits out the
		// connection's own teardown.
		if _, err := conn.ExecContext(context.WithoutCancel(ctx),
			`SELECT pg_advisory_unlock($1, $2)`, int32(lockNamespace), id,
		); err != nil {
			zctx.From(ctx).Warn("release source advisory lock",
				zap.Error(err), zap.String("source", key))
		}
	}()

	return fn(ctx)
}
