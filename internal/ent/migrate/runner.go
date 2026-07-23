// Package migrate contains ent schema definitions, generated client code, and
// a versioned migration runner.
package migrate

import (
	"context"
	"database/sql"
	"embed"
	"io/fs"
	"sort"
	"strings"

	"github.com/go-faster/errors"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Runner applies versioned SQL migrations stored in the embedded filesystem.
type Runner struct {
	db *sql.DB
}

// NewRunner creates a migration runner.
func NewRunner(db *sql.DB) *Runner {
	return &Runner{db: db}
}

// advisoryLockID guards Run against concurrent runners racing on
// schema_migrations (e.g. two migrate Jobs from overlapping helm upgrades).
// Arbitrary but stable across versions.
const advisoryLockID = 0x53534d47 // "SSMG": Sisyphus Schema MiGration

// Run applies all pending migrations in lexical order. It serializes against
// other concurrent callers via a Postgres session advisory lock, so a second
// caller blocks until the first finishes and then finds nothing pending.
func (r *Runner) Run(ctx context.Context) error {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return errors.Wrap(err, "acquire connection")
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, int64(advisoryLockID)); err != nil {
		return errors.Wrap(err, "acquire migration lock")
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, int64(advisoryLockID))
	}()

	return r.run(ctx)
}

func (r *Runner) run(ctx context.Context) error {
	if err := r.ensureTrackingTable(ctx); err != nil {
		return errors.Wrap(err, "create tracking table")
	}

	applied, err := r.appliedVersions(ctx)
	if err != nil {
		return errors.Wrap(err, "read applied versions")
	}

	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return errors.Wrap(err, "read migration dir")
	}

	type migration struct {
		name string
		path string
	}
	var pending []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		if !applied[e.Name()] {
			pending = append(pending, migration{name: e.Name(), path: "migrations/" + e.Name()})
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].name < pending[j].name })

	for _, m := range pending {
		sqlBytes, err := migrationFS.ReadFile(m.path)
		if err != nil {
			return errors.Wrapf(err, "read %s", m.name)
		}

		if err := r.applyMigration(ctx, m.name, string(sqlBytes)); err != nil {
			return errors.Wrapf(err, "apply %s", m.name)
		}
	}

	return nil
}

// Pending returns embedded migration files not yet recorded as applied, in
// lexical order. Unlike Run, it takes no lock and never creates the tracking
// table — it is meant for cheap readiness checks by non-migrating processes,
// not for mutating state. If the tracking table does not exist yet, every
// migration is pending.
func (r *Runner) Pending(ctx context.Context) ([]string, error) {
	exists, err := r.trackingTableExists(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "check tracking table")
	}

	applied := make(map[string]bool)
	if exists {
		applied, err = r.appliedVersions(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "read applied versions")
		}
	}

	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, errors.Wrap(err, "read migration dir")
	}

	var pending []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		if !applied[e.Name()] {
			pending = append(pending, e.Name())
		}
	}
	sort.Strings(pending)
	return pending, nil
}

func (r *Runner) trackingTableExists(ctx context.Context) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.tables WHERE table_name = 'schema_migrations'
	)`).Scan(&exists)
	if err != nil {
		return false, errors.Wrap(err, "query information_schema")
	}
	return exists, nil
}

func (r *Runner) ensureTrackingTable(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`)
	return err
}

func (r *Runner) appliedVersions(ctx context.Context) (map[string]bool, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, errors.Wrap(err, "query schema_migrations")
	}
	defer func() { _ = rows.Close() }()

	applied := make(map[string]bool)
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, errors.Wrap(err, "scan version")
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

func (r *Runner) applyMigration(ctx context.Context, name, sqlContent string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.Wrap(err, "begin tx")
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, sqlContent); err != nil {
		return errors.Wrap(err, "exec migration")
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
		return errors.Wrap(err, "record version")
	}

	if err := tx.Commit(); err != nil {
		return errors.Wrap(err, "commit")
	}

	return nil
}
