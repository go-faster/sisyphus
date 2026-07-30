package main

import (
	stdsql "database/sql"
	"os"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/stretchr/testify/require"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/wire"
)

// The notification endpoints answer 503 unless the handler is built with a
// store, and ssbot's /link, /subscribe and /alerts all go through them — so a
// missing option breaks the entire notification path with no other symptom.
func TestNewHandlerEnablesNotifications(t *testing.T) {
	dsn := os.Getenv("SISYPHUS_TEST_DB")
	if dsn == "" {
		t.Skip("SISYPHUS_TEST_DB not set")
	}
	db, err := stdsql.Open("pgx", dsn)
	require.NoError(t, err)
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.Schema.Create(t.Context()))

	h := newHandler(wire.Components{DB: client}, "test")

	// Read-only, so it needs no fixtures and leaves none behind.
	_, err = h.ListNotifyChats(t.Context())
	require.NoError(t, err, "notify endpoints must be wired, not 503")
}
