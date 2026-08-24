package main

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/apiclient"
	"github.com/go-faster/sisyphus/internal/bot"
	"github.com/go-faster/sisyphus/internal/notify"
)

// notifierAdapter satisfies bot.Notifier over *apiclient.Client. Every
// method but NotifyListSubscriptions delegates directly (signatures match);
// that one needs a type conversion since apiclient.Subscription and
// bot.NotifySubscription are separate types (bot stays apiclient-free).
type notifierAdapter struct {
	api *apiclient.Client
}

func (n notifierAdapter) NotifyEnroll(ctx context.Context, telegramUserID int64) error {
	return n.api.NotifyEnroll(ctx, telegramUserID)
}

func (n notifierAdapter) NotifyPeers(ctx context.Context, peers []bot.NotifyPeer) error {
	out := make([]apiclient.TelegramPeer, 0, len(peers))
	for _, p := range peers {
		out = append(out, apiclient.TelegramPeer{
			PeerType:   p.PeerType,
			PeerID:     p.PeerID,
			AccessHash: p.AccessHash,
			Username:   p.Username,
			Title:      p.Title,
		})
	}
	return n.api.NotifyPeers(ctx, out)
}

func (n notifierAdapter) NotifySubscribe(ctx context.Context, telegramUserID int64, source string, eventTypes []string) error {
	return n.api.NotifySubscribe(ctx, telegramUserID, source, eventTypes)
}

func (n notifierAdapter) NotifyUnsubscribe(ctx context.Context, telegramUserID int64, source string) error {
	return n.api.NotifyUnsubscribe(ctx, telegramUserID, source)
}

func (n notifierAdapter) NotifyListSubscriptions(ctx context.Context, telegramUserID int64) ([]bot.NotifySubscription, error) {
	subs, err := n.api.NotifyListSubscriptions(ctx, telegramUserID)
	if err != nil {
		return nil, err
	}
	out := make([]bot.NotifySubscription, 0, len(subs))
	for _, s := range subs {
		out = append(out, bot.NotifySubscription{Source: s.Source, EventTypes: s.EventTypes, Enabled: s.Enabled})
	}
	return out, nil
}

func (n notifierAdapter) NotifyRegisterChat(ctx context.Context, peerType string, peerID int64, title string, addedBy int64, enabled bool) error {
	return n.api.NotifyRegisterChat(ctx, peerType, peerID, title, addedBy, enabled)
}

func (n notifierAdapter) NotifyListChats(ctx context.Context) ([]bot.NotifyChat, error) {
	chats, err := n.api.NotifyListChats(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]bot.NotifyChat, 0, len(chats))
	for _, c := range chats {
		out = append(out, bot.NotifyChat{PeerType: c.PeerType, PeerID: c.PeerID, Title: c.Title, Enabled: c.Enabled})
	}
	return out, nil
}

const notifyDrainBatchSize = 20

// notifySendInterval resolves the configured pause between two sends: 0 takes
// the default, negative means no pacing at all.
func notifySendInterval(ms int) time.Duration {
	if ms == 0 {
		return notify.DefaultSendInterval
	}
	if ms < 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

// runNotifyDrainLoop polls ssapi for pending Telegram-channel notifications
// and delivers them via b.SendTo, acking each attempt's outcome. It waits
// for b.Ready() so it never calls SendTo before the bot session has
// authenticated. interval <= 0 disables draining (matches notify.poll.
// interval_seconds=0 meaning the whole notification system is off).
//
// send is the pause between two deliveries within one batch; see
// [notify.DefaultSendInterval] for why there is one.
func runNotifyDrainLoop(ctx context.Context, lg *zap.Logger, b *bot.Bot, api *apiclient.Client, interval, send time.Duration) {
	if interval <= 0 {
		return
	}
	select {
	case <-b.Ready():
	case <-ctx.Done():
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		drainPendingNotifications(ctx, lg, b, api, send)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func drainPendingNotifications(ctx context.Context, lg *zap.Logger, b *bot.Bot, api *apiclient.Client, send time.Duration) {
	pending, err := api.PendingNotifications(ctx, notifyDrainBatchSize)
	if err != nil {
		lg.Warn("list pending notifications failed", zap.Error(err))
		return
	}

	for i, n := range pending {
		// Paced, not batched: the whole point is that a burst of events
		// addressed to one person reaches them as a readable sequence. The
		// pause is before each send but the first, so a lone notification is
		// still delivered immediately.
		if i > 0 && send > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(send):
			}
		}
		// The renderer already embeds the URL as a markdown link; appending it
		// again printed every notification's link twice. The buttons are the
		// separate, actionable links the projector picked out.
		sendErr := b.SendTo(ctx, n.ID, n.TelegramPeerType, n.TelegramUserID, n.TelegramAccessHash, n.Text, n.Buttons)
		if sendErr != nil {
			lg.Warn("deliver notification failed", zap.String("notification_id", n.ID.String()), zap.Error(sendErr))
		}
		if ackErr := api.AckNotification(ctx, n.ID, sendErr); ackErr != nil {
			lg.Warn("ack notification failed", zap.String("notification_id", n.ID.String()), zap.Error(ackErr))
		}
	}
}
