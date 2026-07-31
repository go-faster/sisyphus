// Package notify is the notification gateway's destination-side contract. It
// consumes canonical events from the event spine (internal/event) and turns
// them into per-user deliveries: a Projector fans one event.Event into the
// per-recipient notify.Events this system delivers (see subscriber.go), a
// Dispatcher matches those against subscriptions and writes Notifications to an
// outbox, and a Sink delivers one Notification to one user's Target address.
//
// A projector produces data, never message text: a title, a description,
// labels, the buttons worth offering. Turning that into a message is the
// Renderer's job, one text/template per event type (render.go). The split is
// what lets the wording of every notification change in one file instead of
// four, and it is why a projector never composes Markdown — everything it
// puts on an Event is escaped on the way through a template.
//
// Matching a GitLab/Jira actor to a Telegram user needs a stored identity, and
// that identity comes from deployment config alone (`notify.identities`,
// reconciled by ssapi through store.SyncIdentities). A user cannot claim one
// over the bot: nothing they type proves the account is theirs, so a
// self-service /link let anyone name a colleague and read their
// notifications. Subscriptions stay self-service — they only decide what a
// user hears about events already addressed to them.
//
// It depends only on stdlib, google/uuid, and internal/event (itself
// stdlib-only), so it stays import-cycle-free for both the ent-backed store
// (internal/notify/store) and the source collectors + projectors
// (internal/notify/gitlab, .../jira).
package notify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/go-faster/sisyphus/internal/event"
)

// Source identifies which upstream system an Event/Notification came from.
type Source string

const (
	SourceGitLab Source = "gitlab"
	SourceJira   Source = "jira"
	// SourceAlerts covers what the alerting pipeline produces — today an
	// agent investigation of a firing alert. Unlike GitLab and Jira it has no
	// per-user identity to match on: its notifications are broadcast to the
	// chats in deployment config (see Broadcaster).
	SourceAlerts Source = "alerts"
)

// Channel identifies a delivery mechanism (Sink implementation).
type Channel string

const (
	ChannelTelegram Channel = "telegram"
)

// EventType classifies what happened, driving both subscription matching and
// message rendering.
type EventType string

const (
	// EventMRAssigned fires when the recipient is newly added to an MR's
	// assignee list.
	EventMRAssigned EventType = "mr_assigned"
	// EventMRReviewRequested fires when the recipient is newly added to an
	// MR's reviewer list.
	EventMRReviewRequested EventType = "mr_review_requested"
	// EventIssueAssigned fires when the recipient is newly set as a Jira
	// issue's assignee.
	EventIssueAssigned EventType = "issue_assigned"
	// EventMRCommented and EventIssueCommented fire when someone comments on
	// an object already addressed to the recipient — an MR they are assigned
	// to or reviewing, an issue assigned to them. Assignment says work
	// arrived; these say the conversation about it moved.
	EventMRCommented    EventType = "mr_commented"
	EventIssueCommented EventType = "issue_commented"
	// EventMRMentioned and EventIssueMentioned fire when a comment names the
	// recipient — "@username" on GitLab, "[~id]" on Jira. They are separate
	// types rather than a flavor of the commented ones so they can be
	// subscribed to alone: being named is usually more urgent than a comment
	// on something you happen to be assigned, and it does not require being
	// assigned at all.
	EventMRMentioned    EventType = "mr_mentioned"
	EventIssueMentioned EventType = "issue_mentioned"
	// EventInvestigationCompleted fires when an agent investigation finishes.
	// It is addressed to a chat, not a user.
	EventInvestigationCompleted EventType = "investigation_completed"
	// EventAlertFiring and EventAlertResolved announce the alert itself,
	// independently of whether an agent investigates it. Also chat-addressed:
	// an alert names no GitLab or Jira identity to deliver to.
	EventAlertFiring   EventType = "alert_firing"
	EventAlertResolved EventType = "alert_resolved"
)

// LineBreak ends a line inside a notification body.
//
// Two trailing spaces make it a CommonMark *hard* break. A bare "\n" is a
// soft break, which the Telegram renderer turns into a space (correct for
// prose, wrong here) — that collapsed every multi-line notification onto one
// line.
const LineBreak = "  \n"

// Lines joins non-empty parts as separate lines of a notification body.
func Lines(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, LineBreak)
}

// Actor identifies a source-side user, either as the recipient of an Event
// or as whoever caused it. GitLab has no stable numeric id/email in the
// ingested data, so Username is the match key there; Jira's stable key is
// AccountID (see internal/chunk/jira.Issue.AssigneeAccountID).
type Actor struct {
	Source  Source
	Key     string // GitLab: username. Jira: accountId.
	Display string // human-readable name, for rendering only
	// URL is the actor's profile page, for rendering only. Empty when the
	// source did not carry one — a notification renders the bare name then.
	URL string
}

// Event is a single source-side occurrence addressed to a Recipient.
type Event struct {
	Source    Source
	Type      EventType
	Recipient Actor // the source-side user this event is FOR
	Actor     Actor // who caused it (assigner); zero value if unknown
	Title     string
	// Body is optional Markdown detail rendered under Title (an
	// investigation's verdict and findings). Empty for the one-line
	// assignment events. Unlike the fields below it is passed to the
	// renderer as-is: it is already Markdown a projector composed, not a
	// value read out of ingested content.
	Body string
	// Description is the event's lead paragraph in plain text (an alert's
	// description annotation). The renderer escapes it.
	Description string
	// Labels are the identifying key=value pairs worth showing, in the order
	// they should render. The renderer puts them on one monospace line.
	Labels []Label
	// Buttons are the actionable links this event offers, rendered as inline
	// URL buttons under the message. A projector is responsible for putting
	// only vetted URLs here — see the Answers & link buttons rule in the root
	// CLAUDE.md.
	Buttons    []Button
	URL        string
	ObjectID   string // stable id of the parent object, e.g. "group/proj!42"
	EventID    string // stable id of this specific event; see dedup key
	OccurredAt time.Time
}

// Label is one identifying key=value pair on an event: an alert's severity,
// the cluster it fired on.
type Label struct {
	Key   string
	Value string
}

// Button is one inline URL button rendered under a notification.
type Button struct {
	Text string
	URL  string
}

// Valid reports whether b can be rendered as a Telegram URL button: a
// non-empty label pointing at an absolute http(s) URL. It mirrors
// index.Link.Valid, which notify cannot use without taking on its imports.
func (b Button) Valid() bool {
	if strings.TrimSpace(b.Text) == "" {
		return false
	}
	u, err := url.Parse(b.URL)
	if err != nil || u.Host == "" {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

// ValidButtons keeps only the buttons a sink can actually render, dropping
// duplicates by URL. Dispatchers call it on the way to the outbox so an
// unrenderable link never reaches a sink.
func ValidButtons(buttons []Button) []Button {
	if len(buttons) == 0 {
		return nil
	}
	var (
		out  []Button
		seen = make(map[string]struct{}, len(buttons))
	)
	for _, b := range buttons {
		if !b.Valid() {
			continue
		}
		if _, ok := seen[b.URL]; ok {
			continue
		}
		seen[b.URL] = struct{}{}
		out = append(out, Button{Text: strings.TrimSpace(b.Text), URL: b.URL})
	}
	return out
}

// SelfCaused reports whether the event's recipient is also its actor: a user
// should never be notified about their own action.
//
// Identity is (Source, Key) alone. Display and URL are rendering-only and
// need not agree between the two sides — a projector fills the actor's
// profile link but has no reason to fill the recipient's, and comparing whole
// structs would then call an obviously self-caused event someone else's.
func (e Event) SelfCaused() bool {
	return e.Actor.Source != "" && e.Actor.Key != "" &&
		e.Actor.Source == e.Recipient.Source && e.Actor.Key == e.Recipient.Key
}

// Notification is a rendered, user-addressed message ready for a Sink.
type Notification struct {
	UserID uuid.UUID
	Source Source
	Type   EventType
	Text   string
	// Buttons are the rendered message's inline URL buttons, already
	// filtered by ValidButtons.
	Buttons  []Button
	URL      string
	DedupKey string
}

// DedupKey deterministically derives an outbox row's unique key from a user
// and the event that produced it. Even if a collector's cursor diff
// mis-fires and re-emits an already-notified event, the outbox's unique
// dedup_key index makes the resulting insert a no-op — this is the actual
// duplicate-notification guarantee; the cursor is only an efficiency filter.
func DedupKey(userID uuid.UUID, eventID string) string {
	sum := sha256.Sum256([]byte(userID.String() + ":" + eventID))
	return hex.EncodeToString(sum[:])
}

// EventCollector polls its source and returns canonical events new since
// cursor, along with the advanced cursor to persist. It emits one event per
// source occurrence (an MR updated, an issue updated) carrying the object's
// current state; the per-recipient fan-out is the destination's job (Projector,
// see subscriber.go), and duplicate suppression rests on the outbox DedupKey,
// not a collector-side diff. cursor/nextCursor are opaque collector-defined
// JSON, stored the same way ingestion's SyncState.last_cursor is: as an opaque
// string keyed by a Source-specific SyncState row.
type EventCollector interface {
	Source() event.Source
	Collect(ctx context.Context, cursor string) (events []event.Event, nextCursor string, err error)
}

// Target is the sink-specific address resolved from a subscribed user's
// stored identity. A Sink reads only the fields it needs.
type Target struct {
	// TelegramUserID is the peer id: a user id for PeerUser, a channel or
	// group id for PeerChannel/PeerChat.
	TelegramUserID     int64
	TelegramAccessHash int64
	// PeerType says which kind of peer the id names. Empty means PeerUser,
	// so every pre-existing per-user row keeps its meaning.
	PeerType PeerType
}

// PeerType distinguishes the kinds of Telegram peer a Target can address.
// Per-user notifications resolve to a user; broadcasts resolve to the channel
// or group named in deployment config.
type PeerType string

const (
	PeerUser    PeerType = "user"
	PeerChannel PeerType = "channel"
	PeerChat    PeerType = "chat"
)

// Sink delivers one Notification to one Target. Implementations must not
// import ent or any gotd/MTProto type, so they stay unit-testable with a
// fake Target.
type Sink interface {
	Channel() Channel
	Deliver(ctx context.Context, target Target, n Notification) error
}
