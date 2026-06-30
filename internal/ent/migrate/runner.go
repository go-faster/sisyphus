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

// Run applies all pending migrations in lexical order.
func (r *Runner) Run(ctx context.Context) error {
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
