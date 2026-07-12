package agentstore

import (
	"context"
	stdsql "database/sql"
	"os"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/go-faster/errors"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/go-faster/sisyphus/internal/agent"
	"github.com/go-faster/sisyphus/internal/ent"
)

func openTestDB(t *testing.T) *ent.Client {
	t.Helper()
	dsn := os.Getenv("SISYPHUS_TEST_DB")
	if dsn == "" {
		t.Skip("SISYPHUS_TEST_DB not set")
	}

	db, err := stdsql.Open("pgx", dsn)
	require.NoError(t, err)
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	require.NoError(t, client.Schema.Create(ctx))
	t.Cleanup(func() {
		_, _ = client.InvestigationJob.Delete().Exec(context.Background())
	})
	return client
}

func TestStore_SubmitCreatesJob(t *testing.T) {
	store := New(openTestDB(t))
	ctx := t.Context()

	job, created, err := store.Submit(ctx, uuid.NewString(), "something is broken")
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, StatusPending, job.Status)

	got, err := store.Get(ctx, job.ID)
	require.NoError(t, err)
	require.Equal(t, job.ID, got.ID)
	require.Equal(t, StatusPending, got.Status)
}

func TestStore_SubmitSameIdempotencyKeyReturnsExistingJob(t *testing.T) {
	store := New(openTestDB(t))
	ctx := t.Context()
	key := uuid.NewString()

	first, created, err := store.Submit(ctx, key, "description one")
	require.NoError(t, err)
	require.True(t, created)

	second, created, err := store.Submit(ctx, key, "description one")
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, first.ID, second.ID)
}

func TestStore_CompleteAndFailTransitions(t *testing.T) {
	store := New(openTestDB(t))
	ctx := t.Context()

	job, _, err := store.Submit(ctx, uuid.NewString(), "issue")
	require.NoError(t, err)
	require.NoError(t, store.MarkRunning(ctx, job.ID))

	running, err := store.Get(ctx, job.ID)
	require.NoError(t, err)
	require.Equal(t, StatusRunning, running.Status)

	report := agent.Report{Problem: "p", Verdict: agent.VerdictSolved, Findings: "f"}
	require.NoError(t, store.Complete(ctx, job.ID, agent.Result{Report: report, Iterations: 3, ToolsUsed: 2}))

	done, err := store.Get(ctx, job.ID)
	require.NoError(t, err)
	require.Equal(t, StatusDone, done.Status)
	require.Equal(t, report, done.Report)
	require.Equal(t, 3, done.Iterations)
	require.Equal(t, 2, done.ToolsUsed)

	job2, _, err := store.Submit(ctx, uuid.NewString(), "issue 2")
	require.NoError(t, err)
	require.NoError(t, store.Fail(ctx, job2.ID, errors.New("boom")))

	failed, err := store.Get(ctx, job2.ID)
	require.NoError(t, err)
	require.Equal(t, StatusError, failed.Status)
	require.Equal(t, "boom", failed.ErrorMessage)
}

func TestStore_GetUnknownReturnsErrNotFound(t *testing.T) {
	store := New(openTestDB(t))
	_, err := store.Get(t.Context(), uuid.New())
	require.ErrorIs(t, err, ErrNotFound)
}

func TestStore_ReapStaleMarksPendingAndRunningAsError(t *testing.T) {
	store := New(openTestDB(t))
	ctx := t.Context()

	pending, _, err := store.Submit(ctx, uuid.NewString(), "pending job")
	require.NoError(t, err)

	running, _, err := store.Submit(ctx, uuid.NewString(), "running job")
	require.NoError(t, err)
	require.NoError(t, store.MarkRunning(ctx, running.ID))

	done, _, err := store.Submit(ctx, uuid.NewString(), "done job")
	require.NoError(t, err)
	require.NoError(t, store.Complete(ctx, done.ID, agent.Result{Report: agent.Report{Verdict: agent.VerdictSolved}}))

	n, err := store.ReapStale(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, n)

	for _, id := range []uuid.UUID{pending.ID, running.ID} {
		job, err := store.Get(ctx, id)
		require.NoError(t, err)
		require.Equal(t, StatusError, job.Status)
		require.NotEmpty(t, job.ErrorMessage)
	}

	stillDone, err := store.Get(ctx, done.ID)
	require.NoError(t, err)
	require.Equal(t, StatusDone, stillDone.Status)
}
