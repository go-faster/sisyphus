package notify

import (
	"context"

	"github.com/go-faster/errors"

	"github.com/go-faster/sisyphus/internal/event"
)

// Projector turns one canonical event.Event into the per-recipient notify
// Events this system delivers. Each source implements one because only it
// understands the event's Payload shape.
//
// The per-recipient fan-out — one MR update becomes one candidate per
// assignee/reviewer — lives here, not in the source collector. The collector
// emits a source-neutral "it changed, here is its current state" event; the
// Projector expands that to candidates, and the outbox DedupKey (keyed by the
// projected Event.EventID) is what makes re-emitting the same occurrence a
// no-op. So a Projector may safely emit a candidate for every current member
// on every poll: only genuinely new (user, event) pairs become notifications.
type Projector interface {
	Project(e event.Event) ([]Event, error)
}

// RouterSubscriber adapts a Projector plus a Dispatcher into an event.Handler,
// so the notification gateway can subscribe to the event router: it projects
// an incoming event into per-recipient Events and dispatches them to the
// outbox. (Distinct from Subscriber, which is one matched user within a
// dispatch.)
type RouterSubscriber struct {
	projector  Projector
	dispatcher *Dispatcher
}

// NewRouterSubscriber binds projector and dispatcher into an event.Handler.
func NewRouterSubscriber(projector Projector, dispatcher *Dispatcher) *RouterSubscriber {
	return &RouterSubscriber{projector: projector, dispatcher: dispatcher}
}

// Handle implements event.Handler: project e, then dispatch the results.
// Idempotent on e via the outbox DedupKey, as event.Handler requires.
func (s *RouterSubscriber) Handle(ctx context.Context, e event.Event) error {
	events, err := s.projector.Project(e)
	if err != nil {
		return errors.Wrap(err, "project event")
	}
	if _, err := s.dispatcher.Dispatch(ctx, events); err != nil {
		return errors.Wrap(err, "dispatch")
	}
	return nil
}
