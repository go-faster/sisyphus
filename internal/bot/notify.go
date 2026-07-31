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
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/entity"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/tg"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/index"
)

// NotifySubscription describes one of a Telegram user's notification
// subscriptions, as returned by Notifier.NotifyListSubscriptions.
type NotifySubscription struct {
	Source     string
	EventTypes []string
	Enabled    bool
}

// Notifier is the notification-system client the bot needs: enrollment
// (access-hash capture) and subscription management. There is deliberately no
// identity-linking call: who a Telegram user is on GitLab/Jira is decided in
// deployment config (notify.identities), not by what a user types.
// Satisfied by internal/apiclient.Client via a thin adapter in cmd/ssbot
// (the return types don't match exactly, so it's not implemented directly).
type Notifier interface {
	NotifyEnroll(ctx context.Context, telegramUserID int64) error
	NotifyPeers(ctx context.Context, peers []NotifyPeer) error
	NotifySubscribe(ctx context.Context, telegramUserID int64, source string, eventTypes []string) error
	NotifyUnsubscribe(ctx context.Context, telegramUserID int64, source string) error
	NotifyListSubscriptions(ctx context.Context, telegramUserID int64) ([]NotifySubscription, error)
	NotifyRegisterChat(ctx context.Context, peerType string, peerID int64, title string, addedBy int64, enabled bool) error
	NotifyListChats(ctx context.Context) ([]NotifyChat, error)
}

// NotifyPeer is one Telegram peer the bot saw, on its way to storage.
type NotifyPeer struct {
	PeerType   string
	PeerID     int64
	AccessHash int64
	Username   string
	Title      string
}

// NotifyChat is one chat registered to receive broadcast notifications.
type NotifyChat struct {
	PeerType string
	PeerID   int64
	Title    string
	Enabled  bool
}

// errBotNotReady is returned by SendTo before the bot session has
// authenticated (see Ready).
var errBotNotReady = errors.New("bot: session not ready")

// captureNotifyIdentity best-effort persists senderID's current Telegram
// user row on every allowlisted message, so /subscribe has someone to attach
// a subscription to. The addresses themselves are recorded separately by
// capturePeers, for every peer rather than just this sender.
func (b *Bot) captureNotifyIdentity(ctx context.Context, senderID int64) {
	if b.notifier == nil || senderID <= 0 {
		return
	}
	if err := b.notifier.NotifyEnroll(ctx, senderID); err != nil {
		zctx.From(ctx).Warn("notify enroll failed", zap.Error(err))
	}
}

// capturePeers records every peer an update carried, with its access hash.
//
// Proactive on purpose: over MTProto a peer is addressable only with a hash
// that exists in the update that delivered it, and a private channel has no
// username to resolve one from later. Recording only the peers a notification
// command mentioned meant the bot could not message anyone it had not already
// been asked about — including, until this existed, every user who had simply
// never run one.
//
// Best-effort: failing to record a peer must not fail the command the update
// carried.
func (b *Bot) capturePeers(ctx context.Context, e tg.Entities, chat chatPeer) {
	if b.notifier == nil {
		return
	}

	peers := make([]NotifyPeer, 0, len(e.Users)+len(e.Chats)+len(e.Channels)+1)
	for id, u := range e.Users {
		peers = append(peers, NotifyPeer{
			PeerType:   peerTypeUser,
			PeerID:     id,
			AccessHash: u.AccessHash,
			Username:   u.Username,
			Title:      strings.TrimSpace(u.FirstName + " " + u.LastName),
		})
	}
	for id, c := range e.Chats {
		peers = append(peers, NotifyPeer{PeerType: peerTypeChat, PeerID: id, Title: c.Title})
	}
	for id, ch := range e.Channels {
		peers = append(peers, NotifyPeer{
			PeerType:   peerTypeChannel,
			PeerID:     id,
			AccessHash: ch.AccessHash,
			Username:   ch.Username,
			Title:      ch.Title,
		})
	}
	// The chat the update came from may not appear in the entity maps at all
	// (a channel post carries its own peer implicitly), and it is precisely
	// the peer /alerts would register.
	if chat.ID != 0 {
		peers = append(peers, NotifyPeer{
			PeerType:   chat.Type,
			PeerID:     chat.ID,
			AccessHash: chat.AccessHash,
			Title:      chat.Title,
		})
	}

	if err := b.notifier.NotifyPeers(ctx, peers); err != nil {
		zctx.From(ctx).Warn("record telegram peers failed", zap.Error(err))
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
// buttons become inline URL buttons under the message, in one sendMessage
// with the text rather than a follow-up: two messages would double the
// notification and only the first would carry the deduplicating random_id.
func (b *Bot) SendTo(ctx context.Context, notificationID uuid.UUID, peerType string, peerID, accessHash int64, text string, buttons []index.Link) error {
	if b.silent {
		return nil
	}
	sender := b.sender.Load()
	if sender == nil {
		return errBotNotReady
	}
	peer := notifyPeer(peerType, peerID, accessHash)
	randomID := randomIDFor(notificationID)
	request := func() *message.Builder {
		req := sender.To(peer).RandomID(randomID)
		if kb := linksMarkup(buttons); kb != nil {
			req = req.Markup(kb)
		}
		return req
	}
	_, err := request().StyledText(ctx, styling.Custom(func(eb *entity.Builder) error {
		return renderMarkdown(eb, text)
	}))
	if err == nil {
		return nil
	}
	_, err = request().Text(ctx, text)
	return err
}

// notifyPeer resolves an outbox row's target into the peer to send to. A
// broadcast names a channel or a basic group; everything else is a user, which
// is also what an unset peer type means (every row written before broadcasts
// existed).
func notifyPeer(peerType string, peerID, accessHash int64) tg.InputPeerClass {
	switch peerType {
	case "channel":
		return &tg.InputPeerChannel{ChannelID: peerID, AccessHash: accessHash}
	case "chat":
		return &tg.InputPeerChat{ChatID: peerID}
	default:
		return &tg.InputPeerUser{UserID: peerID, AccessHash: accessHash}
	}
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

// defaultEventTypesBySource is what /subscribe enrolls in when the user names
// no types: everything the source can address to them personally. A user who
// finds comment traffic too noisy narrows it by naming types explicitly, which
// is easier to discover than the reverse — nobody guesses at an event type
// they were never subscribed to.
var defaultEventTypesBySource = map[string][]string{
	"gitlab": {"mr_assigned", "mr_review_requested", "mr_commented", "mr_mentioned"},
	"jira":   {"issue_assigned", "issue_commented", "issue_mentioned"},
}

func (b *Bot) handleSubscribeCmd(ctx context.Context, s messageSender, inv invocation) error {
	if b.notifier == nil {
		b.sendTextReply(ctx, s, "Notifications are not configured.")
		return nil
	}
	fields := strings.Fields(inv.Rest)
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

	if err := b.notifier.NotifySubscribe(ctx, inv.SenderID, source, eventTypes); err != nil {
		return errors.Wrap(err, "subscribe")
	}
	b.sendTextReply(ctx, s, fmt.Sprintf("Subscribed to %s: %s", source, strings.Join(eventTypes, ", ")))
	return nil
}

func (b *Bot) handleUnsubscribeCmd(ctx context.Context, s messageSender, inv invocation) error {
	if b.notifier == nil {
		b.sendTextReply(ctx, s, "Notifications are not configured.")
		return nil
	}
	source := strings.ToLower(strings.TrimSpace(inv.Rest))
	if source == "" {
		b.sendTextReply(ctx, s, "Usage: /unsubscribe <gitlab|jira>")
		return nil
	}
	if err := b.notifier.NotifyUnsubscribe(ctx, inv.SenderID, source); err != nil {
		return errors.Wrap(err, "unsubscribe")
	}
	b.sendTextReply(ctx, s, "Unsubscribed from "+source)
	return nil
}

func (b *Bot) handleNotificationsCmd(ctx context.Context, s messageSender, inv invocation) error {
	if b.notifier == nil {
		b.sendTextReply(ctx, s, "Notifications are not configured.")
		return nil
	}
	subs, err := b.notifier.NotifyListSubscriptions(ctx, inv.SenderID)
	if err != nil {
		return errors.Wrap(err, "list subscriptions")
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
