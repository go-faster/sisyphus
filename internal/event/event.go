// Package event defines the canonical cross-context event contract: a single
// Event type describing "something happened at a source", and a Router (see
// router.go) that fans one event out to subscribed destinations — KG ingest,
// notification, and agent. It is the spine that decouples heterogeneous
// sources from shared destinations: a source emits one Event per occurrence,
// and each destination projects it into its own artifact (a Document, a
// Notification, an Investigation).
//
// Like internal/index and internal/notify, this package is intentionally
// dependency-light (stdlib only) so every context can depend on it without
// import cycles. Source-specific detail never leaks into the envelope: it
// lives in the opaque Payload, decoded only by the adapter that owns that
// (Source, Type) pair. The top-level fields are exactly — and only — what
// routing needs, which is what keeps Event from becoming a god-object.
package event

import (
	"encoding/json"
	"time"
)

// Source identifies the upstream system an Event came from.
type Source string

const (
	SourceGitLab       Source = "gitlab"
	SourceJira         Source = "jira"
	SourceTelegram     Source = "telegram"
	SourceAlertmanager Source = "alertmanager"
)

// Type classifies what happened. It drives both subscription matching and each
// destination's projection. The set below is the known vocabulary; Type is a
// plain string so a new source can introduce its own without editing this
// package — but a shared type belongs here for discoverability.
type Type string

const (
	TypeMRUpdated     Type = "mr.updated"
	TypeIssueUpdated  Type = "issue.updated"
	TypeReleased      Type = "released"
	TypeMessagePosted Type = "message.posted"
	TypeAlertFiring   Type = "alert.firing"
	TypeAlertResolved Type = "alert.resolved"
)

// Severity is an optional coarse importance, set by sources that have one
// (e.g. an Alertmanager alert). It lets the agent and notification
// destinations route on urgency without decoding Payload. Empty means unset.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Actor identifies who caused an Event, by their stable source-side key. The
// Event's Source already fixes which system the key belongs to, so Actor does
// not repeat it. The zero value means the cause is unknown.
type Actor struct {
	Key     string // stable source-side id: GitLab username, Jira accountId
	Display string // human-readable name, for rendering only
}

// Zero reports whether the actor is unset.
func (a Actor) Zero() bool { return a == Actor{} }

// Ref points at the source-side object an Event is about (the MR, the issue,
// the alert). ID is stable across occurrences on the same object, so
// destinations can group or supersede events per object.
type Ref struct {
	ID    string // stable object id, e.g. "group/proj!42", a Jira key, an alert fingerprint
	URL   string // canonical http(s) URL, may be empty
	Title string // human-readable, for rendering only
}

// Event is one canonical occurrence at a source, ready to route to any
// destination. It is transient (not itself persisted as knowledge): the KG
// ingest destination turns it into an index.Document, notification turns it
// into per-recipient Notifications, and the agent turns it into an
// Investigation.
type Event struct {
	// ID is the stable id of THIS occurrence and the basis for idempotency: a
	// destination that has already processed an Event.ID must treat a repeat
	// as a no-op. A source that re-emits the same occurrence (cursor overlap)
	// must reuse the same ID.
	ID string

	Source     Source
	Type       Type
	Subject    Ref
	Actor      Actor    // who caused it; zero if unknown
	Severity   Severity // optional coarse urgency; empty if unset
	OccurredAt time.Time

	// Attributes are small, routable facets a destination can match on without
	// decoding Payload (e.g. "project": "group/proj", "label": "incident").
	// Keep them cheap and string-typed; anything structured belongs in Payload.
	Attributes map[string]string

	// Payload is the source-typed body, decoded ONLY by an adapter that owns
	// this (Source, Type). Everything a destination needs generically is in the
	// fields above; Payload carries what only the owning source understands
	// (e.g. the full MR object the notify diff and the ingest normalizer read).
	Payload json.RawMessage
}

// Attr returns the value of a routable attribute, or "" if unset.
func (e Event) Attr(key string) string { return e.Attributes[key] }

// DecodePayload unmarshals the event payload into v. Only the source adapter
// that produced the payload should call this — a destination that does not own
// (Source, Type) must route on the envelope fields instead.
func (e Event) DecodePayload(v any) error { return json.Unmarshal(e.Payload, v) }

// WithPayload returns a copy of e with v JSON-encoded into Payload. Sources use
// it to attach their typed body without hand-marshaling at every call site.
func (e Event) WithPayload(v any) (Event, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return e, err
	}
	e.Payload = b
	return e, nil
}
