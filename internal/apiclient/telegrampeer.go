package apiclient

import (
	"context"
	"time"

	"github.com/go-faster/errors"
	"go.opentelemetry.io/otel/trace"

	"github.com/go-faster/sisyphus/internal/oas"
)

// TelegramPeer is one peer the bot saw, as sent to ssapi.
type TelegramPeer struct {
	PeerType   string
	PeerID     int64
	AccessHash int64
	Username   string
	Title      string
}

// NotifyPeers records peers the bot has seen, with their current access
// hashes. ssbot calls it for every update it processes: a hash exists only in
// the update that carried the peer, so it is captured there or not at all.
func (c *Client) NotifyPeers(ctx context.Context, peers []TelegramPeer) (rerr error) {
	if len(peers) == 0 {
		return nil
	}
	start := time.Now()
	ctx, span := c.tracer.Start(ctx, "apiclient.NotifyPeers", trace.WithSpanKind(trace.SpanKindClient))
	defer func() {
		c.m.record(ctx, "telegram_peers", time.Since(start).Seconds(), 0, rerr)
		span.End()
	}()

	req := &oas.TelegramPeersRequest{Peers: make([]oas.TelegramPeer, 0, len(peers))}
	for _, p := range peers {
		item := oas.TelegramPeer{
			PeerType: oas.TelegramPeerPeerType(p.PeerType),
			PeerID:   p.PeerID,
		}
		if p.AccessHash != 0 {
			item.AccessHash = oas.NewOptInt64(p.AccessHash)
		}
		if p.Username != "" {
			item.Username = oas.NewOptString(p.Username)
		}
		if p.Title != "" {
			item.Title = oas.NewOptString(p.Title)
		}
		req.Peers = append(req.Peers, item)
	}
	if _, err := c.inv.UpsertTelegramPeers(ctx, req); err != nil {
		rerr = errors.Wrap(err, "upsert telegram peers")
	}
	return rerr
}
