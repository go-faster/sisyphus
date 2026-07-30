package store

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/notify"
)

// A broadcast row has no user behind it — the whole point of the alert
// channel — so the outbox must accept one and hand back a peer-typed target.
func TestEnqueueBroadcastWithoutUser(t *testing.T) {
	db := openTestDB(t)
	ctx := t.Context()
	s := New(db, Options{Owner: "broadcast-test"})

	target := notify.Target{TelegramUserID: -1001234567890, TelegramAccessHash: 99, PeerType: notify.PeerChannel}
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
