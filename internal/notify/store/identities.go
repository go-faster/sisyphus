package store

import (
	"context"

	"github.com/go-faster/errors"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/ent/user"
)

// Identity is one Telegram to GitLab/Jira mapping, as an operator declared it.
type Identity struct {
	TelegramUserID  int64
	GitLabUsername  string
	JiraAccountID   string
	JiraDisplayName string
}

// SyncResult reports what [Store.SyncIdentities] changed.
type SyncResult struct {
	// Linked is how many configured identities were written.
	Linked int
	// Cleared is how many users lost a GitLab/Jira identity because the
	// configuration no longer lists them.
	Cleared int
}

// SyncIdentities makes the stored identities match the configured ones, and is
// the only writer of them.
//
// Identity is an authorization decision, not a preference: whoever holds a
// GitLab username receives every notification addressed to it. Nobody can
// prove over Telegram that an account is theirs, so a self-service claim would
// let anyone name a colleague and read their notifications. Deciding it in
// deployment config means the answer comes from someone who already has to be
// trusted.
//
// Removing an entry revokes it, which is why this clears identities it does
// not find: an operator deleting a line expects the mapping to stop, not to
// linger until someone notices. The user row and its subscriptions survive —
// losing an identity means events stop matching, not that the person is
// forgotten.
// Revocations are applied before assignments, in two passes. GitLab usernames
// and Jira accounts are unique, so moving one between two people in a single
// config edit would collide against the old holder if the new one were written
// first.
func (s *Store) SyncIdentities(ctx context.Context, identities []Identity) (SyncResult, error) {
	var res SyncResult

	configured := make(map[int64]Identity, len(identities))
	for _, id := range identities {
		configured[id.TelegramUserID] = id
	}

	linked, err := s.db.User.Query().
		Where(user.Or(user.GitlabUsernameNotNil(), user.JiraAccountIDNotNil())).
		All(ctx)
	if err != nil {
		return res, errors.Wrap(err, "list linked users")
	}
	for _, u := range linked {
		id, stillConfigured := configured[u.TelegramUserID]
		upd := s.db.User.UpdateOneID(u.ID)
		if !stillConfigured || id.GitLabUsername == "" {
			upd.ClearGitlabUsername()
		}
		if !stillConfigured || id.JiraAccountID == "" {
			upd.ClearJiraAccountID()
			upd.ClearJiraDisplayName()
		}
		if err := upd.Exec(ctx); err != nil {
			return res, errors.Wrap(err, "revoke identity")
		}
		if !stillConfigured {
			res.Cleared++
		}
	}

	for _, id := range identities {
		if err := s.applyIdentity(ctx, id); err != nil {
			return res, err
		}
		res.Linked++
	}
	return res, nil
}

// applyIdentity writes the configured identity onto the user's row. The halves
// the config leaves empty were already cleared by the revocation pass.
//
// The row is created if the user has never messaged the bot, but without a
// Telegram access hash — that one comes from a live update (see
// EnrollTelegram), so config must never overwrite it.
func (s *Store) applyIdentity(ctx context.Context, id Identity) error {
	u, err := s.db.User.Query().Where(user.TelegramUserID(id.TelegramUserID)).Only(ctx)
	switch {
	case err == nil:
	case ent.IsNotFound(err):
		created, err := s.db.User.Create().
			SetTelegramUserID(id.TelegramUserID).
			SetEnabled(true).
			Save(ctx)
		if err != nil {
			return errors.Wrap(err, "create user")
		}
		u = created
	default:
		return errors.Wrap(err, "get user")
	}

	upd := s.db.User.UpdateOneID(u.ID)
	if id.GitLabUsername != "" {
		upd.SetGitlabUsername(id.GitLabUsername)
	}
	if id.JiraAccountID != "" {
		upd.SetJiraAccountID(id.JiraAccountID)
	}
	if id.JiraDisplayName != "" {
		upd.SetJiraDisplayName(id.JiraDisplayName)
	}
	if err := upd.Exec(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return errors.Errorf("identity of telegram user %d is already held by another user", id.TelegramUserID)
		}
		return errors.Wrap(err, "write identity")
	}
	return nil
}
