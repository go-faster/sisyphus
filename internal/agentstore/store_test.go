package agentstore

import (
	"context"
	stdsql "database/sql"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/go-faster/errors"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/go-faster/sisyphus/internal/agent"
	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/ent/queuejob"
)

// clock is an injectable time source, so lease expiry is testable without a
// real sleep.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock {
	return &clock{t: time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)}
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

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
		ctx := context.Background()
		_, _ = client.QueueJob.Delete().Where(queuejob.Queue(QueueName)).Exec(ctx)
		_, _ = client.InvestigationJob.Delete().Exec(ctx)
	})
	return client
}

func TestStore_SubmitCreatesJob(t *testing.T) {
	store := New(openTestDB(t), Options{})
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
	store := New(openTestDB(t), Options{})
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
	store := New(openTestDB(t), Options{})
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
	store := New(openTestDB(t), Options{})
	_, err := store.Get(t.Context(), uuid.New())
	require.ErrorIs(t, err, ErrNotFound)
}

// TestStore_ReapStaleSettlesOnlyAbandonedJobs pins the behavior change that
// makes N ssagent replicas safe: reaping must settle jobs the queue gave up
// on, and must NOT touch a job that is merely pending or that another replica
// is still running.
func TestStore_ReapStaleSettlesOnlyAbandonedJobs(t *testing.T) {
	c := newClock()
	store := New(openTestDB(t), Options{MaxAttempts: 1, Lease: time.Minute, Now: c.Now})
	ctx := t.Context()

	abandoned, _, err := store.Submit(ctx, uuid.NewString(), "abandoned job")
	require.NoError(t, err)
	queued, _, err := store.Submit(ctx, uuid.NewString(), "still queued job")
	require.NoError(t, err)

	// Claim the first and never acknowledge it: its worker died.
	claimed, err := store.Queue().Fetch(ctx, 1)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, abandoned.ID, claimed[0].ID)
	require.NoError(t, store.MarkRunning(ctx, abandoned.ID))

	// While its lease is live, nothing is abandoned yet.
	n, err := store.ReapStale(ctx)
	require.NoError(t, err)
	require.Zero(t, n)

	c.Advance(2 * time.Minute)
	n, err = store.ReapStale(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	got, err := store.Get(ctx, abandoned.ID)
	require.NoError(t, err)
	require.Equal(t, StatusError, got.Status)
	require.NotEmpty(t, got.ErrorMessage)

	// The unclaimed job is real outstanding work, not wreckage.
	got, err = store.Get(ctx, queued.ID)
	require.NoError(t, err)
	require.Equal(t, StatusPending, got.Status)
}

// TestStore_SubmitQueuesExactlyOneDelivery verifies a submission is dispatched
// as queue work, and that an idempotent replay does not queue a second run.
func TestStore_SubmitQueuesExactlyOneDelivery(t *testing.T) {
	store := New(openTestDB(t), Options{})
	ctx := t.Context()
	key := uuid.NewString()

	job, created, err := store.Submit(ctx, key, "something is broken")
	require.NoError(t, err)
	require.True(t, created)

	replay, created, err := store.Submit(ctx, key, "something is broken")
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, job.ID, replay.ID)

	claimed, err := store.Queue().Fetch(ctx, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1, "a replayed submission must not queue a second investigation")
	require.Equal(t, job.ID, claimed[0].ID)

	var payload Payload
	require.NoError(t, json.Unmarshal(claimed[0].Payload, &payload))
	require.Equal(t, "something is broken", payload.Description)
}
