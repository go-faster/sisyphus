package event

import (
	"context"
	"errors"
	"slices"
	"sync"
)

// Handler is one destination for events. Handle MUST be idempotent on
// Event.ID: the router (and any durable transport in front of it) delivers
// at-least-once, so a handler may see the same event more than once and must
// produce the same result without double-effect.
type Handler interface {
	Handle(ctx context.Context, e Event) error
}

// HandlerFunc adapts a plain function to Handler.
type HandlerFunc func(ctx context.Context, e Event) error

// Handle implements Handler.
func (f HandlerFunc) Handle(ctx context.Context, e Event) error { return f(ctx, e) }

// Subscription binds a Handler to the events it wants. An empty Sources or
// Types list matches all sources or all types respectively (so a subscription
// with both empty receives every event). Name is used only for diagnostics and
// error attribution.
type Subscription struct {
	Name    string
	Sources []Source
	Types   []Type
	Handler Handler
}

// matches reports whether e should be delivered to this subscription.
func (s Subscription) matches(e Event) bool {
	if len(s.Sources) != 0 && !slices.Contains(s.Sources, e.Source) {
		return false
	}
	if len(s.Types) != 0 && !slices.Contains(s.Types, e.Type) {
		return false
	}
	return true
}

// Router fans one event out to every subscribed destination. Subscriptions are
// registered at wiring time; Route is called per event by a source.
//
// This is the load-bearing contract of the event spine. The interface is
// transport-agnostic on purpose: Mux (below) is a synchronous in-process
// implementation used for wiring and tests, and a later durable, queue-backed
// implementation (one Postgres job row per matching subscription, drained by
// per-destination workers) can satisfy the same interface without any source
// or destination changing.
type Router interface {
	// Subscribe registers a destination. Not safe to call concurrently with
	// Route; register all subscriptions during wiring, then route.
	Subscribe(sub Subscription)
	// Route delivers e to every matching subscription. It is at-least-once:
	// handlers must be idempotent on Event.ID.
	Route(ctx context.Context, e Event) error
}

// Mux is a synchronous, in-process Router: Route invokes each matching
// handler inline and returns when all have run. A failing handler does not
// stop the others — every destination gets its event — and Route returns the
// joined errors. Because handlers are idempotent on Event.ID, a source that
// re-routes after a partial failure is safe.
type Mux struct {
	mu   sync.RWMutex
	subs []Subscription
}

// NewMux returns an empty Mux.
func NewMux() *Mux { return &Mux{} }

// Subscribe registers sub.
func (m *Mux) Subscribe(sub Subscription) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subs = append(m.subs, sub)
}

// Route delivers e to every matching subscription, aggregating handler errors.
func (m *Mux) Route(ctx context.Context, e Event) error {
	m.mu.RLock()
	subs := m.subs
	m.mu.RUnlock()

	var errs []error
	for _, sub := range subs {
		if !sub.matches(e) {
			continue
		}
		if err := sub.Handler.Handle(ctx, e); err != nil {
			errs = append(errs, &subError{name: sub.Name, err: err})
		}
	}
	return errors.Join(errs...)
}

// subError attributes a handler failure to its subscription while preserving
// the original error for errors.Is/As.
type subError struct {
	name string
	err  error
}

func (e *subError) Error() string {
	if e.name == "" {
		return "event handler: " + e.err.Error()
	}
	return "subscription " + e.name + ": " + e.err.Error()
}

func (e *subError) Unwrap() error { return e.err }
