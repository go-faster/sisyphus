package store

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/ent/user"
)

// Suite-distinct ids: the DB-backed suites share one database and run
// concurrently, so a literal collision on the unique telegram_user_id would
// fail another package's test, not this one.
const (
	idAlice = 910001
	idBob   = 910002
)

func TestSyncIdentitiesLinksAndRevokes(t *testing.T) {
	db := openTestDB(t)
	ctx := t.Context()
	s := New(db, Options{Owner: "identities-test"})

	res, err := s.SyncIdentities(ctx, []Identity{
		{TelegramUserID: idAlice, GitLabUsername: "sync-alice", JiraAccountID: "sync-alice-jira", JiraDisplayName: "Alice"},
		{TelegramUserID: idBob, GitLabUsername: "sync-bob"},
	})
	require.NoError(t, err)
	require.Equal(t, 2, res.Linked)

	// A user that never messaged the bot still gets a row, so an event
	// addressed to them matches before their first contact.
	alice := getUser(t, s, idAlice)
	require.Equal(t, "sync-alice", *alice.GitlabUsername)
	require.Equal(t, "Alice", *alice.JiraDisplayName)

	// Dropping only the Jira half revokes it and leaves GitLab alone.
	_, err = s.SyncIdentities(ctx, []Identity{
		{TelegramUserID: idAlice, GitLabUsername: "sync-alice"},
		{TelegramUserID: idBob, GitLabUsername: "sync-bob"},
	})
	require.NoError(t, err)
	alice = getUser(t, s, idAlice)
	require.Nil(t, alice.JiraAccountID)
	require.Nil(t, alice.JiraDisplayName)
	require.Equal(t, "sync-alice", *alice.GitlabUsername)

	// Removing an entry revokes the identity but keeps the user row: losing an
	// identity means events stop matching, not that the person is forgotten.
	res, err = s.SyncIdentities(ctx, []Identity{
		{TelegramUserID: idAlice, GitLabUsername: "sync-alice"},
	})
	require.NoError(t, err)
	require.Equal(t, 1, res.Cleared)
	bob := getUser(t, s, idBob)
	require.Nil(t, bob.GitlabUsername)
}

// A username moving between two people in one edit must not collide with the
// old holder's row, which is why revocations are applied first.
func TestSyncIdentitiesMovesIdentityBetweenUsers(t *testing.T) {
	db := openTestDB(t)
	ctx := t.Context()
	s := New(db, Options{Owner: "identities-test"})

	_, err := s.SyncIdentities(ctx, []Identity{
		{TelegramUserID: idAlice, GitLabUsername: "sync-moving"},
		{TelegramUserID: idBob},
	})
	require.NoError(t, err)

	_, err = s.SyncIdentities(ctx, []Identity{
		{TelegramUserID: idAlice},
		{TelegramUserID: idBob, GitLabUsername: "sync-moving"},
	})
	require.NoError(t, err)

	require.Nil(t, getUser(t, s, idAlice).GitlabUsername)
	require.Equal(t, "sync-moving", *getUser(t, s, idBob).GitlabUsername)
}

func getUser(t *testing.T, s *Store, telegramUserID int64) *ent.User {
	t.Helper()
	u, err := s.db.User.Query().Where(user.TelegramUserID(telegramUserID)).Only(t.Context())
	require.NoError(t, err)
	return u
}
