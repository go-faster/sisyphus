// Package pglock provides try-once Postgres advisory locks for work that must
// run one-at-a-time across processes.
//
// It is the substitute for leader election: a lock is taken for the duration of
// one run and a contended run is skipped, not queued. Callers that need "at most
// one of these in flight anywhere" — an ingestion run advancing a cursor, a
// maintenance sweep — hold one of these and treat [ErrHeld] as a no-op.
package pglock

import (
	"context"
	"database/sql"
	"hash/crc32"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"go.uber.org/zap"
)

// namespace is the classid half of the advisory lock key, keeping these locks in
// their own space. The int64 form used by the schema-migration runner
// (internal/ent/migrate) occupies a different space again, so the two cannot
// collide however the hashes fall.
const namespace = 0x5353494e // "SSIN": SiSyphus INgest

// ErrHeld reports that another process holds the lock, so the run was skipped.
var ErrHeld = errors.New("lock held by another process")

// lockID hashes a key into the objid half of the lock.
//
// A collision between two keys costs mutual exclusion between two runs that did
// not need it — a delayed run, not incorrect data — so a 32-bit hash is the
// right trade against carrying a hand-maintained per-key id table.
func lockID(key string) int32 {
	return int32(crc32.ChecksumIEEE([]byte(key))) //nolint:gosec // deliberate truncation, see above
}

// With runs fn while holding the advisory lock for key, returning [ErrHeld]
// without running fn if another process already holds it.
//
// The lock is session-scoped and held on a dedicated connection, so it spans the
// whole run rather than a transaction — a run can be minutes of HTTP fetching or
// embedding, and must not sit inside an open transaction for its duration.
//
// There is deliberately no heartbeat or lease timeout: Postgres already is the
// lease. A process that dies drops its connection, the backend is terminated,
// and the lock is released with it — including on SIGKILL, an OOM kill or a
// container stop, where no deferred unlock ever runs. Two consequences:
//
//   - A *hung* holder is worse than a dead one. If the socket stays open — a
//     frozen node, a network partition — Postgres cannot tell and will not
//     release until TCP keepalives expire, which is hours under common
//     defaults. That stalls the work rather than corrupting it: a contended run
//     is skipped and retried on its next tick. Tune the DSN's keepalive
//     settings if a deployment needs a tighter bound.
//   - **A transaction-pooling connection pooler breaks this entirely.**
//     pgbouncer in `transaction` mode hands the underlying connection to
//     someone else between statements, so a session-scoped lock is released
//     early or held by an unrelated client. Session-scoped locks require a
//     direct connection or `session` pooling.
func With(ctx context.Context, db *sql.DB, key string, fn func(context.Context) error) error {
	if db == nil {
		// No pooled handle available (some one-shot paths build only an ent
		// client). Running unlocked matches the behavior before locking
		// existed rather than failing the run.
		return fn(ctx)
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return errors.Wrap(err, "acquire connection for lock")
	}
	defer func() { _ = conn.Close() }()

	id := lockID(key)
	var acquired bool
	if err := conn.QueryRowContext(ctx,
		`SELECT pg_try_advisory_lock($1, $2)`, int32(namespace), id,
	).Scan(&acquired); err != nil {
		return errors.Wrap(err, "try advisory lock")
	}
	if !acquired {
		zctx.From(ctx).Info("skipping run, lock held by another process",
			zap.String("lock", key))
		return ErrHeld
	}
	defer func() {
		// Unlock on the same connection, and without the run's context: a run
		// canceled by shutdown must still release, or the lock waits out the
		// connection's own teardown.
		if _, err := conn.ExecContext(context.WithoutCancel(ctx),
			`SELECT pg_advisory_unlock($1, $2)`, int32(namespace), id,
		); err != nil {
			zctx.From(ctx).Warn("release advisory lock",
				zap.Error(err), zap.String("lock", key))
		}
	}()

	return fn(ctx)
}
