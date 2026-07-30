package store

import (
	"context"

	"github.com/go-faster/errors"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/ent/notifychat"
	"github.com/go-faster/sisyphus/internal/notify"
)

// Chat is one registered broadcast destination, as listed back to an operator.
type Chat struct {
	Target  notify.Target
	Title   string
	Enabled bool
	AddedBy int64
}

// RegisterChat records (or re-enables) a chat as a broadcast destination.
//
// The access hash is re-stored on every registration on purpose: a chat's
// hash is per-bot-session, so re-running /alerts on after the session was
// rotated is how a stale peer heals — the same reason enrolling a user
// refreshes theirs.
func (s *Store) RegisterChat(ctx context.Context, target notify.Target, title string, addedBy int64) error {
	create := s.db.NotifyChat.Create().
		SetPeerType(string(peerType(target))).
		SetPeerID(target.TelegramUserID).
		SetTitle(title).
		SetEnabled(true).
		SetAddedBy(addedBy)
	if target.TelegramAccessHash != 0 {
		create = create.SetAccessHash(target.TelegramAccessHash)
	}

	err := create.
		OnConflictColumns(notifychat.FieldPeerType, notifychat.FieldPeerID).
		UpdateNewValues().
		Exec(ctx)
	if err != nil {
		return errors.Wrap(err, "register notify chat")
	}
	return nil
}

// UnregisterChat disables a chat without forgetting it, so its captured
// access hash is still there if it is re-enabled.
func (s *Store) UnregisterChat(ctx context.Context, target notify.Target) (bool, error) {
	n, err := s.db.NotifyChat.Update().
		Where(
			notifychat.PeerType(string(peerType(target))),
			notifychat.PeerID(target.TelegramUserID),
			notifychat.Enabled(true),
		).
		SetEnabled(false).
		Save(ctx)
	if err != nil {
		return false, errors.Wrap(err, "unregister notify chat")
	}
	return n > 0, nil
}

// ListChats returns every registered chat, enabled or not.
func (s *Store) ListChats(ctx context.Context) ([]Chat, error) {
	rows, err := s.db.NotifyChat.Query().Order(ent.Asc(notifychat.FieldCreatedAt)).All(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "query notify chats")
	}
	out := make([]Chat, 0, len(rows))
	for _, row := range rows {
		out = append(out, Chat{
			Target:  chatTarget(row),
			Title:   row.Title,
			Enabled: row.Enabled,
			AddedBy: row.AddedBy,
		})
	}
	return out, nil
}

// BroadcastTargets implements notify.ChatLookup.
func (s *Store) BroadcastTargets(ctx context.Context) ([]notify.Target, error) {
	rows, err := s.db.NotifyChat.Query().
		Where(notifychat.Enabled(true)).
		Order(ent.Asc(notifychat.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "query broadcast chats")
	}
	out := make([]notify.Target, 0, len(rows))
	for _, row := range rows {
		out = append(out, chatTarget(row))
	}
	return out, nil
}

func chatTarget(row *ent.NotifyChat) notify.Target {
	target := notify.Target{
		TelegramUserID: row.PeerID,
		PeerType:       notify.PeerType(row.PeerType),
	}
	if row.AccessHash != nil {
		target.TelegramAccessHash = *row.AccessHash
	}
	return target
}
