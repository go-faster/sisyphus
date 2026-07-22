package bot

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"github.com/google/uuid"
	"github.com/gotd/td/telegram/message/entity"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/tg"
	"go.uber.org/zap"
)

// NotifySubscription describes one of a Telegram user's notification
// subscriptions, as returned by Notifier.NotifyListSubscriptions.
type NotifySubscription struct {
	Source     string
	EventTypes []string
	Enabled    bool
}

// Notifier is the notification-system client the bot needs: enrollment
// (access-hash capture), identity linking, and subscription management.
// Satisfied by internal/apiclient.Client via a thin adapter in cmd/ssbot
// (the return types don't match exactly, so it's not implemented directly).
type Notifier interface {
	NotifyEnroll(ctx context.Context, telegramUserID, accessHash int64) error
	NotifyLinkGitLab(ctx context.Context, telegramUserID int64, username string) error
	NotifyLinkJira(ctx context.Context, telegramUserID int64, accountID, displayName string) error
	NotifySubscribe(ctx context.Context, telegramUserID int64, source string, eventTypes []string) error
	NotifyUnsubscribe(ctx context.Context, telegramUserID int64, source string) error
	NotifyListSubscriptions(ctx context.Context, telegramUserID int64) ([]NotifySubscription, error)
}

// errBotNotReady is returned by SendTo before the bot session has
// authenticated (see Ready).
var errBotNotReady = errors.New("bot: session not ready")

// captureNotifyIdentity best-effort persists senderID's current Telegram
// access hash on every allowlisted message (not just notification
// commands), so a rotated bot session (a new access hash) self-heals on the
// user's next contact instead of requiring re-enrollment via /subscribe.
func (b *Bot) captureNotifyIdentity(ctx context.Context, e tg.Entities, senderID int64) {
	if b.notifier == nil || senderID <= 0 {
		return
	}
	u, ok := e.Users[senderID]
	if !ok {
		return
	}
	if err := b.notifier.NotifyEnroll(ctx, senderID, u.AccessHash); err != nil {
		zctx.From(ctx).Warn("notify enroll failed", zap.Error(err))
	}
}

// SendTo proactively DMs userID (using an accessHash captured by a prior
// enrollment) with text, rendered as Telegram markdown with a plain-text
// fallback if styling fails. This is the only send path in this package
// that isn't a reply to an incoming update; used by ssbot's outbox drain
// loop to deliver internal/notify notifications.
//
// notificationID sets the MTProto request's random_id (via
// randomIDFor(notificationID)) instead of letting gotd pick a fresh random
// one per call: messages.sendMessage's random_id is Telegram's own
// idempotency token — retrying with the same value returns the
// already-sent message instead of creating a duplicate. Without this, a
// drain-loop retry of the same outbox row (e.g. ssbot crashes between
// SendTo succeeding and the row being acked) would DM the user twice.
func (b *Bot) SendTo(ctx context.Context, notificationID uuid.UUID, userID, accessHash int64, text string) error {
	if b.silent {
		return nil
	}
	sender := b.sender.Load()
	if sender == nil {
		return errBotNotReady
	}
	peer := &tg.InputPeerUser{UserID: userID, AccessHash: accessHash}
	randomID := randomIDFor(notificationID)
	_, err := sender.To(peer).RandomID(randomID).StyledText(ctx, styling.Custom(func(eb *entity.Builder) error {
		return renderMarkdown(eb, text)
	}))
	if err == nil {
		return nil
	}
	_, err = sender.To(peer).RandomID(randomID).Text(ctx, text)
	return err
}

// randomIDFor deterministically derives a messages.sendMessage random_id
// from a notification's outbox row ID, so every delivery attempt for the
// same row (retries included) reuses the same value.
func randomIDFor(notificationID uuid.UUID) int64 {
	sum := sha256.Sum256(notificationID[:])
	return int64(binary.BigEndian.Uint64(sum[:8]))
}

// Ready returns a channel closed once the bot session has authenticated and
// SendTo can be used.
func (b *Bot) Ready() <-chan struct{} {
	return b.ready
}

var defaultEventTypesBySource = map[string][]string{
	"gitlab": {"mr_assigned", "mr_review_requested"},
	"jira":   {"issue_assigned"},
}

func (b *Bot) handleLinkCmd(ctx context.Context, s messageSender, senderID int64, rest string) error {
	if b.notifier == nil {
		b.sendTextReply(ctx, s, "Notifications are not configured.")
		return nil
	}
	fields := strings.Fields(rest)
	if len(fields) < 2 {
		b.sendTextReply(ctx, s, "Usage: /link gitlab <username>  or  /link jira <accountId> [display name]")
		return nil
	}

	source := strings.ToLower(fields[0])
	identity := fields[1]

	var err error
	switch source {
	case "gitlab":
		err = b.notifier.NotifyLinkGitLab(ctx, senderID, identity)
	case "jira":
		displayName := strings.Join(fields[2:], " ")
		err = b.notifier.NotifyLinkJira(ctx, senderID, identity, displayName)
	default:
		b.sendTextReply(ctx, s, "Unknown source: "+source+" (expected gitlab or jira)")
		return nil
	}
	if err != nil {
		zctx.From(ctx).Error("notify link failed", zap.Error(err))
		b.sendTextReply(ctx, s, "Failed to link: "+err.Error())
		return nil
	}
	b.sendTextReply(ctx, s, fmt.Sprintf("Linked %s identity: %s", source, identity))
	return nil
}

func (b *Bot) handleSubscribeCmd(ctx context.Context, s messageSender, senderID int64, rest string) error {
	if b.notifier == nil {
		b.sendTextReply(ctx, s, "Notifications are not configured.")
		return nil
	}
	fields := strings.Fields(rest)
	if len(fields) < 1 {
		b.sendTextReply(ctx, s, "Usage: /subscribe <gitlab|jira> [event_type ...]")
		return nil
	}

	source := strings.ToLower(fields[0])
	eventTypes := fields[1:]
	if len(eventTypes) == 0 {
		eventTypes = defaultEventTypesBySource[source]
	}
	if len(eventTypes) == 0 {
		b.sendTextReply(ctx, s, "Unknown source: "+source+" (expected gitlab or jira)")
		return nil
	}

	if err := b.notifier.NotifySubscribe(ctx, senderID, source, eventTypes); err != nil {
		zctx.From(ctx).Error("notify subscribe failed", zap.Error(err))
		b.sendTextReply(ctx, s, "Failed to subscribe: "+err.Error())
		return nil
	}
	b.sendTextReply(ctx, s, fmt.Sprintf("Subscribed to %s: %s", source, strings.Join(eventTypes, ", ")))
	return nil
}

func (b *Bot) handleUnsubscribeCmd(ctx context.Context, s messageSender, senderID int64, rest string) error {
	if b.notifier == nil {
		b.sendTextReply(ctx, s, "Notifications are not configured.")
		return nil
	}
	source := strings.ToLower(strings.TrimSpace(rest))
	if source == "" {
		b.sendTextReply(ctx, s, "Usage: /unsubscribe <gitlab|jira>")
		return nil
	}
	if err := b.notifier.NotifyUnsubscribe(ctx, senderID, source); err != nil {
		zctx.From(ctx).Error("notify unsubscribe failed", zap.Error(err))
		b.sendTextReply(ctx, s, "Failed to unsubscribe: "+err.Error())
		return nil
	}
	b.sendTextReply(ctx, s, "Unsubscribed from "+source)
	return nil
}

func (b *Bot) handleNotificationsCmd(ctx context.Context, s messageSender, senderID int64, _ string) error {
	if b.notifier == nil {
		b.sendTextReply(ctx, s, "Notifications are not configured.")
		return nil
	}
	subs, err := b.notifier.NotifyListSubscriptions(ctx, senderID)
	if err != nil {
		zctx.From(ctx).Error("notify list subscriptions failed", zap.Error(err))
		b.sendTextReply(ctx, s, "Failed to list subscriptions: "+err.Error())
		return nil
	}
	if len(subs) == 0 {
		b.sendTextReply(ctx, s, "No subscriptions. Use /subscribe <gitlab|jira> to add one.")
		return nil
	}

	var sb strings.Builder
	sb.WriteString("Your subscriptions:")
	for _, sub := range subs {
		status := "enabled"
		if !sub.Enabled {
			status = "disabled"
		}
		fmt.Fprintf(&sb, "\n%s (%s): %s", sub.Source, status, strings.Join(sub.EventTypes, ", "))
	}
	b.sendTextReply(ctx, s, sb.String())
	return nil
}
