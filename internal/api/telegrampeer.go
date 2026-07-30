package api

import (
	"context"

	"github.com/go-faster/errors"

	"github.com/go-faster/sisyphus/internal/oas"
	"github.com/go-faster/sisyphus/internal/tgpeer"
)

// PeerStore records the Telegram peers the bot has seen. Satisfied by
// *internal/tgpeer.Store.
type PeerStore interface {
	Upsert(ctx context.Context, peers []tgpeer.Peer) (int, error)
}

// WithPeerStore enables /telegram/peers. Without it the endpoint answers 503,
// like the notification endpoints.
func WithPeerStore(s PeerStore) Option {
	return func(h *Handler) {
		h.peers = s
	}
}

var errPeersNotConfigured = &oas.ErrorStatusCode{
	StatusCode: 503,
	Response:   oas.Error{ErrorMessage: "telegram peer store not configured"},
}

// UpsertTelegramPeers records peers the bot saw in an update.
//
// ssbot has no database, so this is how a peer reaches storage. It is called
// for every update the bot processes, not only for notification commands: an
// access hash exists only in the update that carried the peer, and a peer the
// bot never recorded cannot be messaged later.
func (h *Handler) UpsertTelegramPeers(ctx context.Context, req *oas.TelegramPeersRequest) (*oas.Ack, error) {
	if h.peers == nil {
		return nil, errPeersNotConfigured
	}
	peers := make([]tgpeer.Peer, 0, len(req.Peers))
	for _, p := range req.Peers {
		peers = append(peers, tgpeer.Peer{
			Type:       string(p.PeerType),
			ID:         p.PeerID,
			AccessHash: p.AccessHash.Or(0),
			Username:   p.Username.Or(""),
			Title:      p.Title.Or(""),
		})
	}
	if _, err := h.peers.Upsert(ctx, peers); err != nil {
		return nil, errors.Wrap(err, "upsert telegram peers")
	}
	return &oas.Ack{Ok: true}, nil
}
