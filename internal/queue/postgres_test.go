package queue

import (
	"context"
	stdsql "database/sql"
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

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/ent/queuejob"
)

// clock is an injectable time source: every wait in this package is a
// comparison against a timestamp column, so advancing this is enough to test
// leases and backoff without a single real sleep.
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

// testQueue creates a queue over a name unique to this test, so parallel
// suites sharing the database never see each other's jobs, and cleanup can
// delete exactly this test's rows.
func testQueue(t *testing.T, opts PostgresOptions) (*Postgres, *ent.Client, *clock) {
	t.Helper()
	dsn := os.Getenv("SISYPHUS_TEST_DB")
	if dsn == "" {
		t.Skip("SISYPHUS_TEST_DB not set")
	}

	db, err := stdsql.Open("pgx", dsn)
	require.NoError(t, err)
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.Schema.Create(context.Background()))

	name := "test." + t.Name() + "." + uuid.NewString()
	t.Cleanup(func() {
		_, _ = client.QueueJob.Delete().
			Where(queuejob.Queue(name)).
			Exec(context.Background())
	})

	c := newClock()
	opts.Now = c.Now
	return NewPostgres(client, name, opts), client, c
}

func jobByKey(t *testing.T, client *ent.Client, q *Postgres, key string) *ent.QueueJob {
	t.Helper()
	j, err := client.QueueJob.Query().
		Where(queuejob.Queue(q.name), queuejob.DedupKey(key)).
		Only(t.Context())
	require.NoError(t, err)
	return j
}

func TestPostgres_PublishDedup(t *testing.T) {
	q, client, _ := testQueue(t, PostgresOptions{})
	ctx := t.Context()

	n, err := q.Publish(ctx, Message{Key: "a", Payload: []byte("first")})
	require.NoError(t, err)
	require.Equal(t, 1, n)

	// Same key again is a no-op, and must not clobber the original payload.
	n, err = q.Publish(ctx, Message{Key: "a", Payload: []byte("second")})
	require.NoError(t, err)
	require.Zero(t, n)
	require.Equal(t, []byte("first"), jobByKey(t, client, q, "a").Payload)

	// A batch reports only the messages it actually enqueued.
	n, err = q.Publish(ctx, Message{Key: "a"}, Message{Key: "b"}, Message{Key: "c"})
	require.NoError(t, err)
	require.Equal(t, 2, n)

	_, err = q.Publish(ctx, Message{Key: ""})
	require.Error(t, err)
}

func TestPostgres_FetchAckLifecycle(t *testing.T) {
	q, client, _ := testQueue(t, PostgresOptions{})
	ctx := t.Context()

	_, err := q.Publish(ctx, Message{Key: "a", Payload: []byte("work")})
	require.NoError(t, err)

	got, err := q.Fetch(ctx, 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "a", got[0].Key)
	require.Equal(t, []byte("work"), got[0].Payload)
	require.Equal(t, 1, got[0].Attempts)
	require.False(t, got[0].LastAttempt())

	// A leased job is invisible to a second fetch.
	again, err := q.Fetch(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, again)

	require.NoError(t, q.Ack(ctx, got[0].ID))
	row := jobByKey(t, client, q, "a")
	require.Equal(t, StatusDone, row.Status)
	require.NotNil(t, row.CompletedAt)
	require.Nil(t, row.LeaseExpiresAt)

	// An acked job stays claimed forever, even once the old lease would have
	// lapsed.
	after, err := q.Fetch(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, after)

	require.ErrorIs(t, q.Ack(ctx, uuid.New()), ErrNotFound)
}

func TestPostgres_FetchOrdersOldestFirstAndHonoursLimit(t *testing.T) {
	q, _, c := testQueue(t, PostgresOptions{})
	ctx := t.Context()

	for _, key := range []string{"a", "b", "c"} {
		_, err := q.Publish(ctx, Message{Key: key})
		require.NoError(t, err)
		c.Advance(time.Second)
	}

	got, err := q.Fetch(ctx, 2)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, []string{"a", "b"}, []string{got[0].Key, got[1].Key})
}

func TestPostgres_DelayWithholdsUntilDue(t *testing.T) {
	q, _, c := testQueue(t, PostgresOptions{})
	ctx := t.Context()

	_, err := q.Publish(ctx, Message{Key: "later", Delay: time.Minute})
	require.NoError(t, err)

	got, err := q.Fetch(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, got)

	c.Advance(time.Minute)
	got, err = q.Fetch(ctx, 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
}

func TestPostgres_NackRetriesWithBackoffThenGivesUp(t *testing.T) {
	q, client, c := testQueue(t, PostgresOptions{
		MaxAttempts: 2,
		Backoff:     func(int) time.Duration { return time.Minute },
	})
	ctx := t.Context()

	_, err := q.Publish(ctx, Message{Key: "a"})
	require.NoError(t, err)

	first, err := q.Fetch(ctx, 1)
	require.NoError(t, err)
	require.Len(t, first, 1)
	require.NoError(t, q.Nack(ctx, first[0].ID, errors.New("boom")))

	row := jobByKey(t, client, q, "a")
	require.Equal(t, StatusPending, row.Status)
	require.Equal(t, "boom", row.Error)

	// Held back for the backoff window, then claimable again.
	got, err := q.Fetch(ctx, 1)
	require.NoError(t, err)
	require.Empty(t, got)

	c.Advance(time.Minute)
	second, err := q.Fetch(ctx, 1)
	require.NoError(t, err)
	require.Len(t, second, 1)
	require.Equal(t, 2, second[0].Attempts)
	require.True(t, second[0].LastAttempt())

	// Budget spent: this failure is terminal, not another retry.
	require.NoError(t, q.Nack(ctx, second[0].ID, errors.New("boom again")))
	row = jobByKey(t, client, q, "a")
	require.Equal(t, StatusError, row.Status)
	require.Equal(t, "boom again", row.Error)
	require.NotNil(t, row.CompletedAt)

	c.Advance(time.Hour)
	got, err = q.Fetch(ctx, 1)
	require.NoError(t, err)
	require.Empty(t, got)

	require.ErrorIs(t, q.Nack(ctx, uuid.New(), errors.New("x")), ErrNotFound)
}

func TestPostgres_ExpiredLeaseIsReclaimed(t *testing.T) {
	q, _, c := testQueue(t, PostgresOptions{Lease: time.Minute, MaxAttempts: 3})
	ctx := t.Context()

	_, err := q.Publish(ctx, Message{Key: "a"})
	require.NoError(t, err)

	first, err := q.Fetch(ctx, 1)
	require.NoError(t, err)
	require.Len(t, first, 1)

	// Worker dies here: no ack, no nack. Only the lease lapsing can recover
	// the job.
	c.Advance(30 * time.Second)
	got, err := q.Fetch(ctx, 1)
	require.NoError(t, err)
	require.Empty(t, got)

	c.Advance(31 * time.Second)
	second, err := q.Fetch(ctx, 1)
	require.NoError(t, err)
	require.Len(t, second, 1)
	require.Equal(t, first[0].ID, second[0].ID)
	require.Equal(t, 2, second[0].Attempts)
}

func TestPostgres_ReapStale(t *testing.T) {
	q, client, c := testQueue(t, PostgresOptions{Lease: time.Minute, MaxAttempts: 1})
	ctx := t.Context()

	_, err := q.Publish(ctx, Message{Key: "a"})
	require.NoError(t, err)
	_, err = q.Publish(ctx, Message{Key: "b"})
	require.NoError(t, err)

	// Claim "a" and abandon it; "b" stays pending and untouched.
	got, err := q.Fetch(ctx, 1)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "a", got[0].Key)

	// While the lease is live the job is not stale, however spent its budget.
	n, err := q.ReapStale(ctx)
	require.NoError(t, err)
	require.Zero(t, n)

	c.Advance(2 * time.Minute)
	n, err = q.ReapStale(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	require.Equal(t, StatusError, jobByKey(t, client, q, "a").Status)
	require.Equal(t, StatusPending, jobByKey(t, client, q, "b").Status)
}

func TestPostgres_ConcurrentFetchNeverDoubleClaims(t *testing.T) {
	q, _, _ := testQueue(t, PostgresOptions{})
	ctx := t.Context()

	const jobs = 40
	msgs := make([]Message, 0, jobs)
	for range jobs {
		msgs = append(msgs, Message{Key: uuid.NewString()})
	}
	n, err := q.Publish(ctx, msgs...)
	require.NoError(t, err)
	require.Equal(t, jobs, n)

	const workers = 8
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		seen = make(map[uuid.UUID]int)
	)
	for range workers {
		wg.Go(func() {
			for {
				got, err := q.Fetch(ctx, 5)
				if err != nil || len(got) == 0 {
					return
				}
				mu.Lock()
				for _, d := range got {
					seen[d.ID]++
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	require.Len(t, seen, jobs, "every job claimed exactly once")
	for id, count := range seen {
		require.Equal(t, 1, count, "job %s claimed more than once", id)
	}
}

func TestPostgres_WithTxRollbackDiscardsPublish(t *testing.T) {
	q, client, _ := testQueue(t, PostgresOptions{})
	ctx := t.Context()

	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	n, err := q.WithTx(tx).Publish(ctx, Message{Key: "rolled-back"})
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.NoError(t, tx.Rollback())

	got, err := q.Fetch(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, got, "a rolled-back publish must not leave work behind")

	// The same key is still publishable: the conflicting row never committed.
	tx, err = client.Tx(ctx)
	require.NoError(t, err)
	n, err = q.WithTx(tx).Publish(ctx, Message{Key: "rolled-back"})
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.NoError(t, tx.Commit())

	got, err = q.Fetch(ctx, 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
}
