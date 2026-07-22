package main

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/apiclient"
	"github.com/go-faster/sisyphus/internal/bot"
)

// notifierAdapter satisfies bot.Notifier over *apiclient.Client. Every
// method but NotifyListSubscriptions delegates directly (signatures match);
// that one needs a type conversion since apiclient.Subscription and
// bot.NotifySubscription are separate types (bot stays apiclient-free).
type notifierAdapter struct {
	api *apiclient.Client
}

func (n notifierAdapter) NotifyEnroll(ctx context.Context, telegramUserID, accessHash int64) error {
	return n.api.NotifyEnroll(ctx, telegramUserID, accessHash)
}

func (n notifierAdapter) NotifyLinkGitLab(ctx context.Context, telegramUserID int64, username string) error {
	return n.api.NotifyLinkGitLab(ctx, telegramUserID, username)
}

func (n notifierAdapter) NotifyLinkJira(ctx context.Context, telegramUserID int64, accountID, displayName string) error {
	return n.api.NotifyLinkJira(ctx, telegramUserID, accountID, displayName)
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

const notifyDrainBatchSize = 20

// runNotifyDrainLoop polls ssapi for pending Telegram-channel notifications
// and delivers them via b.SendTo, acking each attempt's outcome. It waits
// for b.Ready() so it never calls SendTo before the bot session has
// authenticated. interval <= 0 disables draining (matches notify.poll.
// interval_seconds=0 meaning the whole notification system is off).
func runNotifyDrainLoop(ctx context.Context, lg *zap.Logger, b *bot.Bot, api *apiclient.Client, interval time.Duration) {
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
		drainPendingNotifications(ctx, lg, b, api)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func drainPendingNotifications(ctx context.Context, lg *zap.Logger, b *bot.Bot, api *apiclient.Client) {
	pending, err := api.PendingNotifications(ctx, notifyDrainBatchSize)
	if err != nil {
		lg.Warn("list pending notifications failed", zap.Error(err))
		return
	}

	for _, n := range pending {
		text := n.Text
		if n.URL != "" {
			text += "\n" + n.URL
		}

		sendErr := b.SendTo(ctx, n.TelegramUserID, n.TelegramAccessHash, text)
		if sendErr != nil {
			lg.Warn("deliver notification failed", zap.String("notification_id", n.ID.String()), zap.Error(sendErr))
		}
		if ackErr := api.AckNotification(ctx, n.ID, sendErr); ackErr != nil {
			lg.Warn("ack notification failed", zap.String("notification_id", n.ID.String()), zap.Error(ackErr))
		}
	}
}
