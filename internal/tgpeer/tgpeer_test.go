package tgpeer

import (
	stdsql "database/sql"
	"os"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/stretchr/testify/require"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/ent/telegrampeer"
)

// Suite-distinct ids: the DB-backed suites share one database and run
// concurrently, so a literal collision on the unique (peer_type, peer_id)
// would fail another package's test.
const (
	idUser    = 940001
	idChannel = -1009400002
	idGroup   = -9400003
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
	require.NoError(t, client.Schema.Create(t.Context()))
	t.Cleanup(func() {
		ctx := t.Context()
		for _, id := range []int64{idUser, idChannel, idGroup} {
			_, _ = client.TelegramPeer.Delete().Where(telegrampeer.PeerID(id)).Exec(ctx)
		}
	})
	return client
}

func TestUpsertAndResolve(t *testing.T) {
	s := New(openTestDB(t), Options{})
	ctx := t.Context()

	n, err := s.Upsert(ctx, []Peer{
		{Type: KindUser, ID: idUser, AccessHash: 111, Username: "alice"},
		{Type: KindChannel, ID: idChannel, AccessHash: 222, Title: "Ops"},
		{Type: KindChat, ID: idGroup, Title: "Team"},
		{Type: "", ID: 1}, // skipped, not an error
		{Type: KindUser},  // skipped, not an error
	})
	require.NoError(t, err)
	require.Equal(t, 3, n)

	hash, found, err := s.Resolve(ctx, KindUser, idUser)
	require.NoError(t, err)
	require.True(t, found)
	require.EqualValues(t, 111, hash)

	// A basic group has no hash and is still a valid address.
	hash, found, err = s.Resolve(ctx, KindChat, idGroup)
	require.NoError(t, err)
	require.True(t, found)
	require.Zero(t, hash)

	_, found, err = s.Resolve(ctx, KindUser, 999999999)
	require.NoError(t, err)
	require.False(t, found)
}

// A rotated bot session gives every peer a new hash; the next update carrying
// it heals the stored one.
func TestUpsertRefreshesHash(t *testing.T) {
	s := New(openTestDB(t), Options{})
	ctx := t.Context()

	_, err := s.Upsert(ctx, []Peer{{Type: KindUser, ID: idUser, AccessHash: 111}})
	require.NoError(t, err)
	_, err = s.Upsert(ctx, []Peer{{Type: KindUser, ID: idUser, AccessHash: 222}})
	require.NoError(t, err)

	hash, _, err := s.Resolve(ctx, KindUser, idUser)
	require.NoError(t, err)
	require.EqualValues(t, 222, hash)
}

// Some updates carry a peer with no hash. Treating that as "the hash is now
// zero" would unaddress a peer the bot could previously reach.
func TestUpsertKeepsHashWhenUpdateOmitsIt(t *testing.T) {
	s := New(openTestDB(t), Options{})
	ctx := t.Context()

	_, err := s.Upsert(ctx, []Peer{{Type: KindChannel, ID: idChannel, AccessHash: 222, Title: "Ops"}})
	require.NoError(t, err)
	_, err = s.Upsert(ctx, []Peer{{Type: KindChannel, ID: idChannel, Title: "Ops renamed"}})
	require.NoError(t, err)

	hash, found, err := s.Resolve(ctx, KindChannel, idChannel)
	require.NoError(t, err)
	require.True(t, found)
	require.EqualValues(t, 222, hash)
}
