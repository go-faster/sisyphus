package bot

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"
)

func alertsInvocation(rest string) invocation {
	return invocation{
		SenderID: 42,
		Chat:     chatPeer{Type: "channel", ID: 1001, AccessHash: 777, Title: "Ops"},
		Rest:     rest,
	}
}

// /alerts on registers the chat it was sent in, with the access hash the
// update carried — the only place a private channel's hash exists.
func TestHandleAlertsCmd_On(t *testing.T) {
	n := newFakeNotifier()
	b := newNotifyTestBot(n)
	c, _ := b.commands.lookup("alerts")
	stub, sent := captureSend(t)

	require.NoError(t, c.handler(context.Background(), stub, alertsInvocation("on")))

	require.Len(t, n.registered, 1)
	require.Equal(t, "channel", n.registered[0].PeerType)
	require.Equal(t, int64(1001), n.registered[0].PeerID)
	require.Equal(t, "Ops", n.registered[0].Title)
	require.True(t, n.registered[0].Enabled)
	require.Equal(t, int64(42), n.lastAddedBy)
	require.Contains(t, *sent, "will receive alert notifications")
}

func TestHandleAlertsCmd_Off(t *testing.T) {
	n := newFakeNotifier()
	b := newNotifyTestBot(n)
	c, _ := b.commands.lookup("alerts")
	stub, sent := captureSend(t)

	require.NoError(t, c.handler(context.Background(), stub, alertsInvocation("off")))
	require.Len(t, n.registered, 1)
	require.False(t, n.registered[0].Enabled)
	require.Contains(t, *sent, "disabled")
}

func TestHandleAlertsCmd_Status(t *testing.T) {
	n := newFakeNotifier()
	n.registered = []NotifyChat{
		{PeerType: "channel", PeerID: 1001, Enabled: true},
		{PeerType: "channel", PeerID: 2002, Enabled: true},
	}
	b := newNotifyTestBot(n)
	c, _ := b.commands.lookup("alerts")
	stub, sent := captureSend(t)

	require.NoError(t, c.handler(context.Background(), stub, alertsInvocation("")))
	require.Contains(t, *sent, "This chat receives alert notifications.")
	require.Contains(t, *sent, "1 other chat(s)")
}

func TestHandleAlertsCmd_StatusUnregistered(t *testing.T) {
	n := newFakeNotifier()
	b := newNotifyTestBot(n)
	c, _ := b.commands.lookup("alerts")
	stub, sent := captureSend(t)

	require.NoError(t, c.handler(context.Background(), stub, alertsInvocation("status")))
	require.Contains(t, *sent, "not registered")
	require.Empty(t, n.registered)
}

func TestHandleAlertsCmd_UnknownArg(t *testing.T) {
	n := newFakeNotifier()
	b := newNotifyTestBot(n)
	c, _ := b.commands.lookup("alerts")
	stub, sent := captureSend(t)

	require.NoError(t, c.handler(context.Background(), stub, alertsInvocation("maybe")))
	require.Contains(t, *sent, "Usage: /alerts")
	require.Empty(t, n.registered)
}

// The peer, including its access hash, has to come out of the update's
// entities: it is what makes a private channel addressable later.
func TestChatPeerFrom(t *testing.T) {
	e := tg.Entities{
		Channels: map[int64]*tg.Channel{5: {ID: 5, AccessHash: 99, Title: "Ops"}},
		Chats:    map[int64]*tg.Chat{7: {ID: 7, Title: "Group"}},
		Users:    map[int64]*tg.User{9: {ID: 9, AccessHash: 11, FirstName: "Ann"}},
	}

	require.Equal(t, chatPeer{Type: "channel", ID: 5, AccessHash: 99, Title: "Ops"},
		chatPeerFrom(e, &tg.PeerChannel{ChannelID: 5}))
	require.Equal(t, chatPeer{Type: "chat", ID: 7, Title: "Group"},
		chatPeerFrom(e, &tg.PeerChat{ChatID: 7}), "a basic group needs no access hash")
	require.Equal(t, chatPeer{Type: "user", ID: 9, AccessHash: 11, Title: "Ann"},
		chatPeerFrom(e, &tg.PeerUser{UserID: 9}))

	// An unknown peer still yields its id, so the command can report that it
	// could not identify the chat rather than registering a bogus one.
	require.Equal(t, chatPeer{Type: "channel", ID: 404},
		chatPeerFrom(tg.Entities{}, &tg.PeerChannel{ChannelID: 404}))
}
