package store

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/notify"
	"github.com/go-faster/sisyphus/internal/tgpeer"
)

// A broadcast row has no user behind it — the whole point of the alert
// channel — so the outbox must accept one and hand back a peer-typed target.
func TestEnqueueBroadcastWithoutUser(t *testing.T) {
	db := openTestDB(t)
	ctx := t.Context()
	s := New(db, Options{Owner: "broadcast-test"})

	target := notify.Target{TelegramUserID: -1001234567890, TelegramAccessHash: 99, PeerType: notify.PeerChannel}
	// The hash is resolved at delivery from the peer store, not copied into
	// the outbox row.
	_, err := tgpeer.New(db, tgpeer.Options{}).Upsert(ctx, []tgpeer.Peer{
		{Type: tgpeer.KindChannel, ID: target.TelegramUserID, AccessHash: target.TelegramAccessHash},
	})
	require.NoError(t, err)
	n := notify.Notification{
		Source:   notify.SourceAlerts,
		Type:     notify.EventInvestigationCompleted,
		Text:     "*Investigation:* HighErrorRate",
		DedupKey: notify.TargetDedupKey(target, "investigation:abc"),
	}

	created, err := s.Enqueue(ctx, notify.ChannelTelegram, target, n)
	require.NoError(t, err)
	require.True(t, created)

	// Same event, same chat: the dedup key collapses it, exactly as it does
	// for a per-user notification.
	created, err = s.Enqueue(ctx, notify.ChannelTelegram, target, n)
	require.NoError(t, err)
	require.False(t, created)

	pending, err := s.Pending(ctx, notify.ChannelTelegram, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, target.TelegramUserID, pending[0].TelegramUserID)
	require.Equal(t, target.TelegramAccessHash, pending[0].TelegramAccessHash)
	require.Equal(t, notify.PeerChannel, pending[0].TelegramPeerType)

	require.NoError(t, s.Ack(ctx, pending[0].ID, nil))
}

// Rows written before broadcasts existed carry no peer type; they must still
// deliver to a user.
func TestPendingDefaultsPeerTypeToUser(t *testing.T) {
	db := openTestDB(t)
	ctx := t.Context()
	s := New(db, Options{Owner: "broadcast-test"})

	target := notify.Target{TelegramUserID: 4242, TelegramAccessHash: 7}
	_, err := s.Enqueue(ctx, notify.ChannelTelegram, target, notify.Notification{
		Source:   notify.SourceAlerts,
		Type:     notify.EventInvestigationCompleted,
		Text:     "hello",
		DedupKey: notify.TargetDedupKey(target, "investigation:no-peer-type"),
	})
	require.NoError(t, err)

	pending, err := s.Pending(ctx, notify.ChannelTelegram, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, notify.PeerUser, pending[0].TelegramPeerType)

	require.NoError(t, s.Ack(ctx, pending[0].ID, nil))
}

// A chat registers itself from inside the chat, and only enabled chats are
// broadcast to.
func TestRegisterAndListChats(t *testing.T) {
	db := openTestDB(t)
	ctx := t.Context()
	s := New(db, Options{Owner: "broadcast-test"})

	channel := notify.Target{TelegramUserID: -1009000001, TelegramAccessHash: 77, PeerType: notify.PeerChannel}
	group := notify.Target{TelegramUserID: -9000002, PeerType: notify.PeerChat}

	peers := tgpeer.New(db, tgpeer.Options{})
	_, err := peers.Upsert(ctx, []tgpeer.Peer{
		{Type: tgpeer.KindChannel, ID: channel.TelegramUserID, AccessHash: channel.TelegramAccessHash},
		{Type: tgpeer.KindChat, ID: group.TelegramUserID},
	})
	require.NoError(t, err)

	require.NoError(t, s.RegisterChat(ctx, channel, "Ops", 42))
	require.NoError(t, s.RegisterChat(ctx, group, "Team", 42))

	targets, err := s.BroadcastTargets(ctx)
	require.NoError(t, err)
	require.Contains(t, targets, channel)
	require.Contains(t, targets, group, "a basic group has no access hash and still resolves")

	// A rotated access hash heals through the peer store, on any update that
	// carries the channel — the registration itself does not hold it.
	rotated := channel
	rotated.TelegramAccessHash = 88
	_, err = peers.Upsert(ctx, []tgpeer.Peer{
		{Type: tgpeer.KindChannel, ID: rotated.TelegramUserID, AccessHash: rotated.TelegramAccessHash},
	})
	require.NoError(t, err)
	require.NoError(t, s.RegisterChat(ctx, rotated, "Ops renamed", 43))

	targets, err = s.BroadcastTargets(ctx)
	require.NoError(t, err)
	require.Contains(t, targets, rotated)
	require.NotContains(t, targets, channel)

	// Disabling keeps the row (and its hash) but stops delivery.
	off, err := s.UnregisterChat(ctx, rotated)
	require.NoError(t, err)
	require.True(t, off)

	targets, err = s.BroadcastTargets(ctx)
	require.NoError(t, err)
	require.NotContains(t, targets, rotated)

	chats, err := s.ListChats(ctx)
	require.NoError(t, err)
	var found bool
	for _, c := range chats {
		if c.Target.TelegramUserID == rotated.TelegramUserID {
			found = true
			require.False(t, c.Enabled)
			require.Equal(t, "Ops renamed", c.Title)
			require.Equal(t, int64(43), c.AddedBy)
		}
	}
	require.True(t, found, "a disabled chat is still listed")

	// Disabling twice reports that nothing changed.
	off, err = s.UnregisterChat(ctx, rotated)
	require.NoError(t, err)
	require.False(t, off)
}
