package notify

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type fakeLookup struct {
	subs map[string][]Subscriber // key: source|eventType|recipientKey
}

func lookupKey(source Source, eventType EventType, recipient Actor) string {
	return string(source) + "|" + string(eventType) + "|" + recipient.Key
}

func (f *fakeLookup) Subscribers(_ context.Context, source Source, eventType EventType, recipient Actor) ([]Subscriber, error) {
	return f.subs[lookupKey(source, eventType, recipient)], nil
}

type fakeOutbox struct {
	created []Notification
	seen    map[string]bool
}

func (f *fakeOutbox) Enqueue(_ context.Context, _ Channel, _ Target, n Notification) (bool, error) {
	if f.seen == nil {
		f.seen = make(map[string]bool)
	}
	if f.seen[n.DedupKey] {
		return false, nil
	}
	f.seen[n.DedupKey] = true
	f.created = append(f.created, n)
	return true, nil
}

func TestDispatcher_FansOutToSubscribers(t *testing.T) {
	user1, user2 := uuid.New(), uuid.New()
	recipient := Actor{Source: SourceGitLab, Key: "alice"}
	lookup := &fakeLookup{subs: map[string][]Subscriber{
		lookupKey(SourceGitLab, EventMRAssigned, recipient): {
			{UserID: user1, Target: Target{TelegramUserID: 1}},
			{UserID: user2, Target: Target{TelegramUserID: 2}},
		},
	}}
	outbox := &fakeOutbox{}
	d := NewDispatcher(lookup, outbox, ChannelTelegram, nil)

	n, err := d.Dispatch(t.Context(), []Event{{
		Source:    SourceGitLab,
		Type:      EventMRAssigned,
		Recipient: recipient,
		Actor:     Actor{Source: SourceGitLab, Key: "bob"},
		Title:     "MR !1: Fix bug",
		URL:       "https://example.com/mr/1",
		EventID:   "gitlab_mr_assign:group/proj!1:alice",
	}})
	require.NoError(t, err)
	require.Equal(t, 2, n)
	require.Len(t, outbox.created, 2)
}

func TestDispatcher_NoSubscribersIsNoOp(t *testing.T) {
	lookup := &fakeLookup{}
	outbox := &fakeOutbox{}
	d := NewDispatcher(lookup, outbox, ChannelTelegram, nil)

	n, err := d.Dispatch(t.Context(), []Event{{
		Source:    SourceGitLab,
		Type:      EventMRAssigned,
		Recipient: Actor{Source: SourceGitLab, Key: "nobody"},
		EventID:   "gitlab_mr_assign:group/proj!1:nobody",
	}})
	require.NoError(t, err)
	require.Zero(t, n)
	require.Empty(t, outbox.created)
}

func TestDispatcher_SkipsSelfCausedEvents(t *testing.T) {
	user1 := uuid.New()
	recipient := Actor{Source: SourceGitLab, Key: "alice"}
	lookup := &fakeLookup{subs: map[string][]Subscriber{
		lookupKey(SourceGitLab, EventMRAssigned, recipient): {
			{UserID: user1, Target: Target{TelegramUserID: 1}},
		},
	}}
	outbox := &fakeOutbox{}
	d := NewDispatcher(lookup, outbox, ChannelTelegram, nil)

	n, err := d.Dispatch(t.Context(), []Event{{
		Source:    SourceGitLab,
		Type:      EventMRAssigned,
		Recipient: recipient,
		Actor:     recipient, // assigned themselves
		EventID:   "gitlab_mr_assign:group/proj!1:alice",
	}})
	require.NoError(t, err)
	require.Zero(t, n)
	require.Empty(t, outbox.created)
}

func TestDispatcher_DuplicateEventIsIdempotent(t *testing.T) {
	user1 := uuid.New()
	recipient := Actor{Source: SourceGitLab, Key: "alice"}
	lookup := &fakeLookup{subs: map[string][]Subscriber{
		lookupKey(SourceGitLab, EventMRAssigned, recipient): {
			{UserID: user1, Target: Target{TelegramUserID: 1}},
		},
	}}
	outbox := &fakeOutbox{}
	d := NewDispatcher(lookup, outbox, ChannelTelegram, nil)

	event := Event{
		Source:    SourceGitLab,
		Type:      EventMRAssigned,
		Recipient: recipient,
		Actor:     Actor{Source: SourceGitLab, Key: "bob"},
		EventID:   "gitlab_mr_assign:group/proj!1:alice",
	}

	n1, err := d.Dispatch(t.Context(), []Event{event})
	require.NoError(t, err)
	require.Equal(t, 1, n1)

	// Re-dispatching the same event (e.g. a collector re-emitting after a
	// cursor rewind) enqueues nothing new: DedupKey collides on the outbox.
	n2, err := d.Dispatch(t.Context(), []Event{event})
	require.NoError(t, err)
	require.Zero(t, n2)
	require.Len(t, outbox.created, 1)
}

func TestDedupKey_DeterministicAndUserScoped(t *testing.T) {
	u1, u2 := uuid.New(), uuid.New()
	require.Equal(t, DedupKey(u1, "event-a"), DedupKey(u1, "event-a"))
	require.NotEqual(t, DedupKey(u1, "event-a"), DedupKey(u2, "event-a"))
	require.NotEqual(t, DedupKey(u1, "event-a"), DedupKey(u1, "event-b"))
}

func TestEvent_SelfCaused(t *testing.T) {
	a := Actor{Source: SourceGitLab, Key: "alice"}
	b := Actor{Source: SourceGitLab, Key: "bob"}
	require.True(t, Event{Recipient: a, Actor: a}.SelfCaused())
	require.False(t, Event{Recipient: a, Actor: b}.SelfCaused())
	require.False(t, Event{Recipient: a}.SelfCaused()) // unknown actor
}
