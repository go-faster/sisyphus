package apiclient

import (
	"context"
	"time"

	"github.com/go-faster/errors"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/go-faster/sisyphus/internal/oas"
)

// NotifyEnroll upserts a Telegram user's NotifyUser row and access hash.
// Called on /subscribe and on every allowlisted message from a known user,
// so a rotated bot session self-heals on the user's next contact.
func (c *Client) NotifyEnroll(ctx context.Context, telegramUserID, accessHash int64) (rerr error) {
	start := time.Now()
	defer func() { c.m.record(ctx, "notify_enroll", time.Since(start).Seconds(), 0, rerr) }()

	_, err := c.inv.NotifyEnroll(ctx, &oas.NotifyEnrollRequest{
		TelegramUserID:     telegramUserID,
		TelegramAccessHash: accessHash,
	})
	if err != nil {
		rerr = errors.Wrap(err, "notify enroll")
	}
	return rerr
}

// NotifyLinkGitLab links telegramUserID to a GitLab username.
func (c *Client) NotifyLinkGitLab(ctx context.Context, telegramUserID int64, username string) (rerr error) {
	start := time.Now()
	defer func() { c.m.record(ctx, "notify_link_gitlab", time.Since(start).Seconds(), 0, rerr) }()

	_, err := c.inv.NotifyLink(ctx, &oas.NotifyLinkRequest{
		TelegramUserID: telegramUserID,
		Source:         oas.NotifyLinkRequestSourceGitlab,
		Identity:       username,
	})
	if err != nil {
		rerr = errors.Wrap(err, "notify link gitlab")
	}
	return rerr
}

// NotifyLinkJira links telegramUserID to a Jira accountId.
func (c *Client) NotifyLinkJira(ctx context.Context, telegramUserID int64, accountID, displayName string) (rerr error) {
	start := time.Now()
	defer func() { c.m.record(ctx, "notify_link_jira", time.Since(start).Seconds(), 0, rerr) }()

	req := &oas.NotifyLinkRequest{
		TelegramUserID: telegramUserID,
		Source:         oas.NotifyLinkRequestSourceJira,
		Identity:       accountID,
	}
	if displayName != "" {
		req.DisplayName = oas.NewOptString(displayName)
	}
	_, err := c.inv.NotifyLink(ctx, req)
	if err != nil {
		rerr = errors.Wrap(err, "notify link jira")
	}
	return rerr
}

// NotifySubscribe subscribes telegramUserID to source's event types.
func (c *Client) NotifySubscribe(ctx context.Context, telegramUserID int64, source string, eventTypes []string) (rerr error) {
	start := time.Now()
	defer func() { c.m.record(ctx, "notify_subscribe", time.Since(start).Seconds(), 0, rerr) }()

	_, err := c.inv.NotifySubscribe(ctx, &oas.NotifySubscribeRequest{
		TelegramUserID: telegramUserID,
		Source:         oas.NotifySubscribeRequestSource(source),
		EventTypes:     eventTypes,
	})
	if err != nil {
		rerr = errors.Wrap(err, "notify subscribe")
	}
	return rerr
}

// NotifyUnsubscribe unsubscribes telegramUserID from source.
func (c *Client) NotifyUnsubscribe(ctx context.Context, telegramUserID int64, source string) (rerr error) {
	start := time.Now()
	defer func() { c.m.record(ctx, "notify_unsubscribe", time.Since(start).Seconds(), 0, rerr) }()

	_, err := c.inv.NotifyUnsubscribe(ctx, &oas.NotifyUnsubscribeRequest{
		TelegramUserID: telegramUserID,
		Source:         oas.NotifyUnsubscribeRequestSource(source),
	})
	if err != nil {
		rerr = errors.Wrap(err, "notify unsubscribe")
	}
	return rerr
}

// Subscription describes one of a Telegram user's subscriptions.
type Subscription struct {
	Source     string
	EventTypes []string
	Enabled    bool
}

// NotifyListSubscriptions lists telegramUserID's subscriptions.
func (c *Client) NotifyListSubscriptions(ctx context.Context, telegramUserID int64) (_ []Subscription, rerr error) {
	start := time.Now()
	defer func() { c.m.record(ctx, "notify_list_subscriptions", time.Since(start).Seconds(), 0, rerr) }()

	resp, err := c.inv.NotifyListSubscriptions(ctx, oas.NotifyListSubscriptionsParams{TelegramUserID: telegramUserID})
	if err != nil {
		rerr = errors.Wrap(err, "notify list subscriptions")
		return nil, rerr
	}
	out := make([]Subscription, 0, len(resp.Subscriptions))
	for _, s := range resp.Subscriptions {
		out = append(out, Subscription{Source: s.Source, EventTypes: s.EventTypes, Enabled: s.Enabled})
	}
	return out, nil
}

// PendingNotification is one outbox row ready for a sink to deliver.
type PendingNotification struct {
	ID                 uuid.UUID
	TelegramUserID     int64
	TelegramAccessHash int64
	Text               string
	URL                string
	Attempts           int
}

// PendingNotifications drains up to limit pending Telegram-channel
// notifications, oldest first.
func (c *Client) PendingNotifications(ctx context.Context, limit int) (_ []PendingNotification, rerr error) {
	start := time.Now()
	ctx, span := c.tracer.Start(ctx, "apiclient.PendingNotifications", trace.WithSpanKind(trace.SpanKindClient))
	defer func() {
		c.m.record(ctx, "pending_notifications", time.Since(start).Seconds(), 0, rerr)
		span.End()
	}()

	var params oas.GetPendingNotificationsParams
	if limit > 0 {
		params.Limit = oas.NewOptInt32(int32(limit))
	}
	resp, err := c.inv.GetPendingNotifications(ctx, params)
	if err != nil {
		rerr = errors.Wrap(err, "get pending notifications")
		return nil, rerr
	}
	out := make([]PendingNotification, 0, len(resp.Notifications))
	for _, n := range resp.Notifications {
		out = append(out, PendingNotification{
			ID:                 n.ID,
			TelegramUserID:     n.TelegramUserID,
			TelegramAccessHash: n.TelegramAccessHash,
			Text:               n.Text,
			URL:                n.URL.Or(""),
			Attempts:           n.Attempts,
		})
	}
	span.SetAttributes(attribute.Int("notifications.count", len(out)))
	return out, nil
}

// AckNotification records a delivery attempt's outcome for notification id.
// deliverErr nil means delivered; non-nil records the failure and increments
// the outbox row's attempt count.
func (c *Client) AckNotification(ctx context.Context, id uuid.UUID, deliverErr error) (rerr error) {
	start := time.Now()
	defer func() { c.m.record(ctx, "ack_notification", time.Since(start).Seconds(), 0, rerr) }()

	req := &oas.NotificationAckRequest{Status: oas.NotificationAckRequestStatusDelivered}
	if deliverErr != nil {
		req.Status = oas.NotificationAckRequestStatusError
		req.Error = oas.NewOptString(deliverErr.Error())
	}
	_, err := c.inv.AckNotification(ctx, req, oas.AckNotificationParams{ID: id})
	if err != nil {
		rerr = errors.Wrap(err, "ack notification")
	}
	return rerr
}
