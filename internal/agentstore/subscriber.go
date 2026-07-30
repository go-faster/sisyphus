package agentstore

import (
	"context"
	"slices"
	"sort"
	"strings"

	"github.com/go-faster/errors"

	"github.com/go-faster/sisyphus/internal/event"
)

// Submitter is the part of [Store] a [Subscriber] needs, kept as an interface
// so the routing decision can be tested without Postgres.
type Submitter interface {
	Submit(ctx context.Context, idempotencyKey, description string) (Job, bool, error)
}

// SubscriberOptions configures which events become investigations.
type SubscriberOptions struct {
	// Types the subscriber reacts to. Empty means event.TypeAlertFiring only:
	// a resolved alert has nothing left to investigate, and an MR update is
	// not worth an LLM loop.
	Types []event.Type
	// MinSeverity drops events below this urgency. Empty means no floor —
	// but note that an event with no severity at all is never dropped, since
	// only some sources set one.
	MinSeverity event.Severity
}

func (opts *SubscriberOptions) setDefaults() {
	if len(opts.Types) == 0 {
		opts.Types = []event.Type{event.TypeAlertFiring}
	}
}

// Subscriber is the agent destination on the event spine: it turns a
// qualifying event into a queued investigation.
//
// Idempotency is the job row's own: the submit key is the Event.ID, so a
// source that re-emits an occurrence (Alertmanager re-sending a firing alert
// every repeat_interval) reuses the existing job instead of starting a second
// investigation of the same thing.
type Subscriber struct {
	submitter Submitter
	opts      SubscriberOptions
}

// NewSubscriber binds a submitter into an event.Handler.
func NewSubscriber(submitter Submitter, opts SubscriberOptions) *Subscriber {
	opts.setDefaults()
	return &Subscriber{submitter: submitter, opts: opts}
}

// Handle implements event.Handler.
func (s *Subscriber) Handle(ctx context.Context, e event.Event) error {
	if !s.wants(e) {
		return nil
	}
	if _, _, err := s.submitter.Submit(ctx, e.ID, Describe(e)); err != nil {
		return errors.Wrap(err, "submit investigation")
	}
	return nil
}

// wants reports whether e qualifies for an investigation.
func (s *Subscriber) wants(e event.Event) bool {
	if !slices.Contains(s.opts.Types, e.Type) {
		return false
	}
	if s.opts.MinSeverity == "" {
		return true
	}
	return severityRank(e.Severity) >= severityRank(s.opts.MinSeverity)
}

// severityRank orders severities so MinSeverity can be a simple floor. An
// unset severity ranks at the top rather than the bottom: a source that
// reports no severity has not said "this is unimportant", and silently
// dropping its events would make the floor a source filter by accident.
//
// Only ever applied to an event's severity — an unset MinSeverity means "no
// floor" and is short-circuited by the caller, since ranking it here would
// make the default the strictest setting instead of the loosest.
func severityRank(s event.Severity) int {
	switch s {
	case event.SeverityInfo:
		return 1
	case event.SeverityWarning:
		return 2
	case event.SeverityCritical:
		return 3
	default:
		return 4
	}
}

// Describe renders the investigation prompt for an event: what happened,
// where, and every routable facet the source attached. The agent gets no
// special-cased alert schema — it reads this text and decides what to look at.
func Describe(e event.Event) string {
	var b strings.Builder
	b.WriteString("Investigate this ")
	b.WriteString(string(e.Source))
	b.WriteString(" event: ")
	b.WriteString(string(e.Type))
	if e.Subject.Title != "" {
		b.WriteString(" — ")
		b.WriteString(e.Subject.Title)
	}
	b.WriteString(".\n")

	if e.Severity != "" {
		b.WriteString("Severity: ")
		b.WriteString(string(e.Severity))
		b.WriteString("\n")
	}
	if !e.OccurredAt.IsZero() {
		b.WriteString("Occurred at: ")
		b.WriteString(e.OccurredAt.UTC().String())
		b.WriteString("\n")
	}
	if e.Subject.URL != "" {
		b.WriteString("Source URL: ")
		b.WriteString(e.Subject.URL)
		b.WriteString("\n")
	}

	if len(e.Attributes) > 0 {
		keys := make([]string, 0, len(e.Attributes))
		for k := range e.Attributes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("Attributes:\n")
		for _, k := range keys {
			b.WriteString("- ")
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(e.Attributes[k])
			b.WriteString("\n")
		}
	}
	return b.String()
}
