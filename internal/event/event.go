// Package event defines the canonical cross-context event contract: a single
// Event type describing "something happened at a source", and a Router (see
// router.go) that fans one event out to subscribed destinations — KG ingest,
// notification, and agent. It is the spine that decouples heterogeneous
// sources from shared destinations: a source emits one Event per occurrence,
// and each destination projects it into its own artifact (a Document, a
// Notification, an Investigation).
//
// A payload is versioned by the adapter that owns it (see Event.PayloadVersion
// and DecodePayload): the envelope is generic and stable, while the opaque
// body is exactly where a shape change can reach a reader that predates or
// postdates it.
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
	"strconv"
	"time"
)

// Source identifies the upstream system an Event came from.
type Source string

const (
	SourceGitLab       Source = "gitlab"
	SourceJira         Source = "jira"
	SourceTelegram     Source = "telegram"
	SourceAlertmanager Source = "alertmanager"
	// SourceAgent is the agent itself, which is both a destination (it
	// investigates events) and a source (its finished investigation is an
	// occurrence other destinations react to).
	SourceAgent Source = "agent"
)

// Type classifies what happened. It drives both subscription matching and each
// destination's projection. The set below is the known vocabulary; Type is a
// plain string so a new source can introduce its own without editing this
// package — but a shared type belongs here for discoverability.
type Type string

const (
	TypeMRUpdated Type = "mr.updated"
	// TypeMRConflict says a merge request cannot be merged into its target
	// branch right now. It is separate from TypeMRUpdated because it is not
	// an update at all: it is usually caused by a change to the *target*
	// branch, which leaves the MR's own updated_at untouched, so it is found
	// by a sweep rather than by the incremental poll.
	TypeMRConflict    Type = "mr.conflict"
	TypeIssueUpdated  Type = "issue.updated"
	TypeReleased      Type = "released"
	TypeMessagePosted Type = "message.posted"
	TypeAlertFiring   Type = "alert.firing"
	TypeAlertResolved Type = "alert.resolved"
	// TypeInvestigationCompleted is emitted by the agent when a queued
	// investigation finishes, carrying its report.
	TypeInvestigationCompleted Type = "investigation.completed"
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
	// URL is the actor's profile page, for rendering only. Empty when the
	// source did not carry one; a destination must not synthesize it, since
	// the shape differs per deployment (Jira Cloud and Server/DC do not
	// address a user the same way).
	URL string
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

	// PayloadVersion is the schema version of Payload, owned by the adapter
	// that produces it: it counts changes to that one (Source, Type)'s payload
	// shape, not changes to this envelope, so two sources' versions are
	// unrelated numbers.
	//
	// It exists because an event outlives the process that wrote it. Most are
	// routed in-process and decoded a microsecond later, but an alert becomes
	// an investigation's queued trigger and can be drained by a replica
	// running the next release. A field whose Go type changed decodes there
	// as a silent wrong answer or a cryptic type error; the version turns
	// both into [PayloadVersionError], naming the shape that was written and
	// the one this binary understands.
	//
	// Zero means the producer set none, which for a persisted event means it
	// predates versioning. It is deliberately not defaulted to 1: an
	// unversioned payload is exactly the one whose shape nothing vouches for.
	PayloadVersion int
}

// PayloadVersionError reports a payload written in a shape the reader does not
// understand. Consumers recover it with errors.As to log what was found — the
// numbers are the whole diagnostic, and a bare "decode failed" is what this
// type exists to stop.
type PayloadVersionError struct {
	Source Source
	Type   Type
	Got    int // the version the payload was written with; 0 if unversioned
	Want   int // the version the reader understands
}

func (e *PayloadVersionError) Error() string {
	return "payload version " + strconv.Itoa(e.Got) + " for " + string(e.Source) + "/" + string(e.Type) +
		": this build understands " + strconv.Itoa(e.Want)
}

// Attr returns the value of a routable attribute, or "" if unset.
func (e Event) Attr(key string) string { return e.Attributes[key] }

// DecodePayload unmarshals the event payload into v, provided it was written
// in the version the caller understands. Only the adapter that owns this
// (Source, Type) should call it — a destination that does not must route on
// the envelope fields instead.
//
// want is stated at the call site rather than inferred from v because the
// version belongs to the shape, not to the Go type currently modeling it:
// renaming a field or changing its type leaves the same struct decoding a
// payload it can no longer read. A reader that genuinely handles several
// shapes switches on [Event.PayloadVersion] and calls this once per version.
func (e Event) DecodePayload(want int, v any) error {
	if e.PayloadVersion != want {
		return &PayloadVersionError{Source: e.Source, Type: e.Type, Got: e.PayloadVersion, Want: want}
	}
	return json.Unmarshal(e.Payload, v)
}

// WithPayload returns a copy of e with v JSON-encoded into Payload, stamped
// with the version v was written in. Sources use it to attach their typed body
// without hand-marshaling at every call site.
//
// The version is a parameter rather than a field the caller may set on the
// literal so that attaching a payload and declaring its shape are one act: a
// payload that reaches a reader unstamped is indistinguishable from one
// written before versioning existed.
func (e Event) WithPayload(version int, v any) (Event, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return e, err
	}
	e.Payload = b
	e.PayloadVersion = version
	return e, nil
}
