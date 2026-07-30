package bot

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-faster/errors"
)

// handleAlertsCmd registers the chat the command was sent in as a destination
// for broadcast notifications — today an agent's investigation of a firing
// alert.
//
// It deliberately acts on the *current* chat rather than on an id given as an
// argument. The peer's access hash only exists in the update that carried
// this command, so registering from inside the channel is what makes a
// private one addressable at all; asking an operator to paste an id and a
// hash into config would be both worse and, for a private channel, wrong.
//
// The bot must be an admin in a channel to receive the command there in the
// first place, so "can run /alerts on here" already means "is in the channel
// with rights".
func (b *Bot) handleAlertsCmd(ctx context.Context, s messageSender, inv invocation) error {
	if b.notifier == nil {
		b.sendTextReply(ctx, s, "Notifications are not configured.")
		return nil
	}
	if inv.Chat.ID == 0 {
		b.sendTextReply(ctx, s, "Could not identify this chat.")
		return nil
	}

	switch strings.ToLower(strings.TrimSpace(inv.Rest)) {
	case "on":
		return b.setAlerts(ctx, s, inv, true)
	case "off":
		return b.setAlerts(ctx, s, inv, false)
	case "", "status":
		return b.alertsStatus(ctx, s, inv)
	default:
		b.sendTextReply(ctx, s, "Usage: /alerts <on|off|status> — run it in the chat that should receive alerts")
		return nil
	}
}

func (b *Bot) setAlerts(ctx context.Context, s messageSender, inv invocation, enabled bool) error {
	err := b.notifier.NotifyRegisterChat(ctx, inv.Chat.Type, inv.Chat.ID, inv.Chat.Title, inv.SenderID, enabled)
	if err != nil {
		return errors.Wrap(err, "register chat")
	}
	if enabled {
		b.sendTextReply(ctx, s, "This chat will receive alert notifications. Turn them off with /alerts off.")
		return nil
	}
	b.sendTextReply(ctx, s, "Alert notifications disabled for this chat.")
	return nil
}

func (b *Bot) alertsStatus(ctx context.Context, s messageSender, inv invocation) error {
	chats, err := b.notifier.NotifyListChats(ctx)
	if err != nil {
		return errors.Wrap(err, "list chats")
	}

	var here string
	var others int
	for _, c := range chats {
		switch {
		case c.PeerID == inv.Chat.ID && c.PeerType == inv.Chat.Type:
			if c.Enabled {
				here = "This chat receives alert notifications."
			} else {
				here = "Alert notifications are off for this chat."
			}
		case c.Enabled:
			others++
		}
	}
	if here == "" {
		here = "This chat is not registered. Run /alerts on to register it."
	}
	if others > 0 {
		here += fmt.Sprintf("\n%d other chat(s) also receive them.", others)
	}
	b.sendTextReply(ctx, s, here)
	return nil
}
