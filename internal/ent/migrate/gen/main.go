// Command gen generates a new versioned SQL migration file by diffing the
// ent schema (internal/ent/schema, source of truth) against the migrations
// already applied to a throwaway Postgres container. Run after changing the
// ent schema:
//
//	go run ./internal/ent/migrate/gen <name>
//
// Requires a local Docker daemon; the container is created and removed for
// this run only, nothing persists and no already-running stack is needed.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	atlas "ariga.io/atlas/sql/migrate"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql/schema"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	entmigrate "github.com/go-faster/scpbot/internal/ent/migrate"
)

func init() {
	// atlas opens the diff target via database/sql using the driver name
	// matching the DSN scheme ("postgres"); register pgx under that name
	// instead of adding a second postgres driver dependency (lib/pq).
	sql.Register("postgres", stdlib.GetDefaultDriver())
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 2 {
		return fmt.Errorf("usage: gen <name>")
	}
	name := os.Args[1]

	ctx := context.Background()
	container, err := tcpostgres.Run(ctx,
		"postgres:17-alpine",
		tcpostgres.WithDatabase("scpbot_migrate_scratch"),
		tcpostgres.WithUsername("scpbot"),
		tcpostgres.WithPassword("scpbot"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		return fmt.Errorf("start scratch postgres container: %w", err)
	}
	defer func() { _ = testcontainers.TerminateContainer(container) }()

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return fmt.Errorf("scratch postgres connection string: %w", err)
	}

	dir, err := atlas.NewLocalDir("internal/ent/migrate/migrations")
	if err != nil {
		return fmt.Errorf("open migration directory: %w", err)
	}

	return entmigrate.NamedDiff(ctx, dsn, name,
		schema.WithDir(dir),
		schema.WithMigrationMode(schema.ModeReplay),
		schema.WithDialect(dialect.Postgres),
		schema.WithFormatter(atlas.DefaultFormatter),
	)
}
