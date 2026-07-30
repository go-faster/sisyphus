package notify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"github.com/go-faster/errors"

	"github.com/go-faster/sisyphus/internal/event"
)

// Broadcaster writes one outbox row per configured Target, with no subscribed
// user behind it.
//
// This is the second addressing mode of the notification gateway. The
// Dispatcher's mode is per-user: an event names a recipient, and only users
// who linked that identity and subscribed get a row. Some events name nobody —
// an alert investigation is addressed to whoever watches the team channel, and
// asking every member to link an identity and subscribe would be the wrong
// question. Those go to Targets from deployment config instead.
//
// Everything after the addressing is identical: the same outbox, the same
// DedupKey guarantee, the same sink draining it.
type Broadcaster struct {
	Chats   ChatLookup
	Outbox  OutboxWriter
	Render  Renderer
	Channel Channel
}

// ChatLookup resolves the chats a broadcast goes to.
//
// The list is data, not configuration: a chat registers itself when someone
// runs /alerts on inside it, which is the only moment its MTProto peer —
// access hash included — is available without an operator copying numbers
// into a config file. A private channel has no username to resolve later, so
// capturing the peer at registration time is what makes one addressable at
// all.
type ChatLookup interface {
	BroadcastTargets(ctx context.Context) ([]Target, error)
}

// NewBroadcaster creates a Broadcaster delivering over channel to whichever
// chats are registered, using DefaultRenderer unless render is non-nil.
func NewBroadcaster(chats ChatLookup, outbox OutboxWriter, channel Channel, render Renderer) *Broadcaster {
	if render == nil {
		render = DefaultRenderer{}
	}
	return &Broadcaster{Chats: chats, Outbox: outbox, Render: render, Channel: channel}
}

// Dispatch enqueues one Notification per (event, target) pair.
func (b *Broadcaster) Dispatch(ctx context.Context, events []Event) (enqueued int, err error) {
	if len(events) == 0 {
		return 0, nil
	}
	targets, err := b.Chats.BroadcastTargets(ctx)
	if err != nil {
		return 0, errors.Wrap(err, "lookup broadcast chats")
	}
	// No chat has registered: nothing to announce to, which is not an error.
	// The report is on the job row either way.
	if len(targets) == 0 {
		return 0, nil
	}
	for _, e := range events {
		text, err := b.Render.Render(e)
		if err != nil {
			return enqueued, errors.Wrap(err, "render event")
		}
		for _, target := range targets {
			n := Notification{
				Source:   e.Source,
				Type:     e.Type,
				Text:     text,
				URL:      e.URL,
				DedupKey: TargetDedupKey(target, e.EventID),
			}
			created, err := b.Outbox.Enqueue(ctx, b.Channel, target, n)
			if err != nil {
				return enqueued, errors.Wrap(err, "enqueue notification")
			}
			if created {
				enqueued++
			}
		}
	}
	return enqueued, nil
}

// TargetDedupKey is [DedupKey] for a row with no user behind it: the chat
// takes the user's place, so re-emitting an event still collapses to one
// message per chat.
func TargetDedupKey(target Target, eventID string) string {
	sum := sha256.Sum256([]byte(
		string(target.PeerType) + ":" + strconv.FormatInt(target.TelegramUserID, 10) + ":" + eventID,
	))
	return hex.EncodeToString(sum[:])
}

// BroadcastSubscriber adapts a Projector plus a Broadcaster into an
// event.Handler, the unaddressed counterpart of [RouterSubscriber].
type BroadcastSubscriber struct {
	projector   Projector
	broadcaster *Broadcaster
}

// NewBroadcastSubscriber binds projector and broadcaster into an event.Handler.
func NewBroadcastSubscriber(projector Projector, broadcaster *Broadcaster) *BroadcastSubscriber {
	return &BroadcastSubscriber{projector: projector, broadcaster: broadcaster}
}

// Handle implements event.Handler. Idempotent on e via the outbox DedupKey.
func (s *BroadcastSubscriber) Handle(ctx context.Context, e event.Event) error {
	events, err := s.projector.Project(e)
	if err != nil {
		return errors.Wrap(err, "project event")
	}
	if _, err := s.broadcaster.Dispatch(ctx, events); err != nil {
		return errors.Wrap(err, "broadcast")
	}
	return nil
}
