// Package tgpeer stores what the bot knows about Telegram peers: the access
// hash that makes a peer addressable over MTProto, and the names that identify
// it to a human.
//
// It exists as its own concern because an access hash is not a fact about a
// notification, a subscription or a person. It is a property of a peer as seen
// by one bot session: it rotates when the session does, a private channel has
// no other way to be addressed at all, and every send path needs it. Keeping
// it on the notification tables meant only someone who had run a notify
// command was addressable, which is exactly backwards — the bot learns a peer
// the moment it sees any update mentioning it.
package tgpeer

import (
	"context"
	"time"

	"github.com/go-faster/errors"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/ent/telegrampeer"
)

// Peer kinds, matching notify.PeerType's values.
const (
	KindUser    = "user"
	KindChat    = "chat"    // basic group
	KindChannel = "channel" // channel or supergroup
)

// Peer is one Telegram peer as the bot last saw it.
type Peer struct {
	Type       string
	ID         int64
	AccessHash int64
	Username   string
	Title      string
}

// Store persists peers via ent.
type Store struct {
	db  *ent.Client
	now func() time.Time
}

// Options configures a [Store].
type Options struct {
	// Now overrides the clock, for tests only.
	Now func() time.Time
}

// New creates a Store backed by db.
func New(db *ent.Client, opts Options) *Store {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Store{db: db, now: opts.Now}
}

// Upsert records peers, refreshing the access hash and names of ones already
// known. Peers without a type or id are skipped rather than rejected: they
// come from whatever an update happened to carry, and one unusable entry must
// not discard the rest of the batch.
//
// A zero access hash never overwrites a stored one. Some updates carry a peer
// with no hash (a basic group has none; a partial entity may omit it), and
// treating that as "the hash is now zero" would unaddress a peer the bot could
// previously reach.
func (s *Store) Upsert(ctx context.Context, peers []Peer) (int, error) {
	var n int
	for _, p := range peers {
		if p.Type == "" || p.ID == 0 {
			continue
		}
		if err := s.upsertOne(ctx, p); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (s *Store) upsertOne(ctx context.Context, p Peer) error {
	existing, err := s.db.TelegramPeer.Query().
		Where(telegrampeer.PeerType(p.Type), telegrampeer.PeerID(p.ID)).
		Only(ctx)
	switch {
	case ent.IsNotFound(err):
		create := s.db.TelegramPeer.Create().
			SetPeerType(p.Type).
			SetPeerID(p.ID).
			SetLastSeenAt(s.now())
		if p.AccessHash != 0 {
			create.SetAccessHash(p.AccessHash)
		}
		if p.Username != "" {
			create.SetUsername(p.Username)
		}
		if p.Title != "" {
			create.SetTitle(p.Title)
		}
		if err := create.Exec(ctx); err != nil {
			// Two updates about the same peer can race; the loser just reads
			// the winner's row on the next update.
			if ent.IsConstraintError(err) {
				return nil
			}
			return errors.Wrap(err, "create telegram peer")
		}
		return nil
	case err != nil:
		return errors.Wrap(err, "query telegram peer")
	}

	upd := s.db.TelegramPeer.UpdateOneID(existing.ID).SetLastSeenAt(s.now())
	if p.AccessHash != 0 {
		upd.SetAccessHash(p.AccessHash)
	}
	if p.Username != "" {
		upd.SetUsername(p.Username)
	}
	if p.Title != "" {
		upd.SetTitle(p.Title)
	}
	if err := upd.Exec(ctx); err != nil {
		return errors.Wrap(err, "update telegram peer")
	}
	return nil
}

// Resolve returns the stored access hash for a peer. found is false when the
// bot has never seen it; a known peer with no hash (a basic group) returns 0
// and true, which is a valid address.
func (s *Store) Resolve(ctx context.Context, peerType string, peerID int64) (hash int64, found bool, err error) {
	row, err := s.db.TelegramPeer.Query().
		Where(telegrampeer.PeerType(peerType), telegrampeer.PeerID(peerID)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, errors.Wrapf(err, "resolve telegram peer %s:%d", peerType, peerID)
	}
	if row.AccessHash == nil {
		return 0, true, nil
	}
	return *row.AccessHash, true, nil
}
