package notify

import (
	"context"

	"github.com/go-faster/errors"
	"github.com/google/uuid"
)

// Subscriber is one user matched to an Event by SubscriptionLookup, along
// with the Target address to notify at.
type Subscriber struct {
	UserID uuid.UUID
	Target Target
}

// SubscriptionLookup resolves which internal users are subscribed to an
// event's (source, type), among those whose linked identity matches the
// event's Recipient.
type SubscriptionLookup interface {
	Subscribers(ctx context.Context, source Source, eventType EventType, recipient Actor) ([]Subscriber, error)
}

// OutboxWriter persists a rendered Notification for delivery over channel.
// Enqueue must be idempotent on n.DedupKey: created reports whether this
// call inserted a new row, so a Dispatcher never double-counts a duplicate.
type OutboxWriter interface {
	Enqueue(ctx context.Context, channel Channel, target Target, n Notification) (created bool, err error)
}

// Renderer turns an Event into the notification text shown to the user. The
// text is Telegram-flavored Markdown; see render.go for the default.
type Renderer interface {
	Render(e Event) (text string, err error)
}

// Dispatcher matches Events to subscribed users and writes one outbox row
// per (event, subscriber) pair. It runs in the process that owns both the
// DB and the source fetchers (ssingest serve), not in the sink's process.
type Dispatcher struct {
	Lookup  SubscriptionLookup
	Outbox  OutboxWriter
	Render  Renderer
	Channel Channel
}

// NewDispatcher creates a Dispatcher delivering over channel, using
// DefaultRenderer unless render is non-nil.
func NewDispatcher(lookup SubscriptionLookup, outbox OutboxWriter, channel Channel, render Renderer) *Dispatcher {
	if render == nil {
		render = DefaultRenderer{}
	}
	return &Dispatcher{Lookup: lookup, Outbox: outbox, Render: render, Channel: channel}
}

// Dispatch enqueues a Notification for every subscriber matched to each
// event, skipping self-caused events (Event.SelfCaused).
func (d *Dispatcher) Dispatch(ctx context.Context, events []Event) (enqueued int, err error) {
	for _, e := range events {
		if e.SelfCaused() {
			continue
		}

		subs, err := d.Lookup.Subscribers(ctx, e.Source, e.Type, e.Recipient)
		if err != nil {
			return enqueued, errors.Wrap(err, "lookup subscribers")
		}
		if len(subs) == 0 {
			continue
		}

		text, err := d.Render.Render(e)
		if err != nil {
			return enqueued, errors.Wrap(err, "render event")
		}

		buttons := ValidButtons(e.Buttons)
		for _, sub := range subs {
			n := Notification{
				UserID:   sub.UserID,
				Source:   e.Source,
				Type:     e.Type,
				Text:     text,
				Buttons:  buttons,
				URL:      e.URL,
				DedupKey: DedupKey(sub.UserID, e.EventID),
			}
			created, err := d.Outbox.Enqueue(ctx, d.Channel, sub.Target, n)
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
