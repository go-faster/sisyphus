package notify

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/event"
)

type broadcastOutbox struct {
	rows map[string]Notification
	tgts map[string]Target
}

func newBroadcastOutbox() *broadcastOutbox {
	return &broadcastOutbox{rows: map[string]Notification{}, tgts: map[string]Target{}}
}

func (f *broadcastOutbox) Enqueue(_ context.Context, _ Channel, target Target, n Notification) (bool, error) {
	if _, ok := f.rows[n.DedupKey]; ok {
		return false, nil
	}
	f.rows[n.DedupKey] = n
	f.tgts[n.DedupKey] = target
	return true, nil
}

func TestBroadcasterDispatch(t *testing.T) {
	out := newBroadcastOutbox()
	b := NewBroadcaster(out, ChannelTelegram, []Target{
		{TelegramUserID: -1001, TelegramAccessHash: 7, PeerType: PeerChannel},
		{TelegramUserID: -42, PeerType: PeerChat},
	}, nil)

	e := Event{
		Source:  SourceAlerts,
		Type:    EventInvestigationCompleted,
		Title:   "HighErrorRate",
		Body:    "Verdict: solved",
		URL:     "https://prometheus.example.com/graph",
		EventID: "investigation:abc",
	}

	n, err := b.Dispatch(context.Background(), []Event{e})
	require.NoError(t, err)
	require.Equal(t, 2, n, "one row per configured chat")

	// Re-dispatching the same event is a no-op: the dedup key is per (chat,
	// event), so the chat never sees the same report twice.
	n, err = b.Dispatch(context.Background(), []Event{e})
	require.NoError(t, err)
	require.Zero(t, n)
	require.Len(t, out.rows, 2)

	for _, row := range out.rows {
		require.Contains(t, row.Text, "HighErrorRate")
		require.Contains(t, row.Text, "Verdict: solved")
		require.Equal(t, SourceAlerts, row.Source)
		require.Zero(t, row.UserID, "a broadcast row is addressed by target, not by user")
	}
}

func TestBroadcasterNoTargets(t *testing.T) {
	out := newBroadcastOutbox()
	b := NewBroadcaster(out, ChannelTelegram, nil, nil)
	n, err := b.Dispatch(context.Background(), []Event{{EventID: "x"}})
	require.NoError(t, err)
	require.Zero(t, n)
	require.Empty(t, out.rows)
}

// Two chats must not share a dedup key, or only the first would get the
// message.
func TestTargetDedupKeyDistinctPerChat(t *testing.T) {
	a := Target{TelegramUserID: -1001, PeerType: PeerChannel}
	b := Target{TelegramUserID: -1002, PeerType: PeerChannel}
	require.NotEqual(t, TargetDedupKey(a, "e1"), TargetDedupKey(b, "e1"))
	require.NotEqual(t, TargetDedupKey(a, "e1"), TargetDedupKey(a, "e2"))
	require.Equal(t, TargetDedupKey(a, "e1"), TargetDedupKey(a, "e1"))
}

type stubProjector struct{ events []Event }

func (s stubProjector) Project(event.Event) ([]Event, error) { return s.events, nil }

func TestBroadcastSubscriber(t *testing.T) {
	out := newBroadcastOutbox()
	b := NewBroadcaster(out, ChannelTelegram, []Target{{TelegramUserID: -1001, PeerType: PeerChannel}}, nil)
	sub := NewBroadcastSubscriber(stubProjector{events: []Event{{
		Source: SourceAlerts, Type: EventInvestigationCompleted, Title: "t", EventID: "e1",
	}}}, b)

	require.NoError(t, sub.Handle(context.Background(), event.Event{ID: "e1"}))
	require.NoError(t, sub.Handle(context.Background(), event.Event{ID: "e1"}))
	require.Len(t, out.rows, 1)
}
