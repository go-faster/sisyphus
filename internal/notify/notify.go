// Package notify is the notification gateway's destination-side contract. It
// consumes canonical events from the event spine (internal/event) and turns
// them into per-user deliveries: a Projector fans one event.Event into the
// per-recipient notify.Events this system delivers (see subscriber.go), a
// Dispatcher matches those against subscriptions and writes Notifications to an
// outbox, and a Sink delivers one Notification to one user's Target address.
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
	"time"

	"github.com/google/uuid"

	"github.com/go-faster/sisyphus/internal/event"
)

// Source identifies which upstream system an Event/Notification came from.
type Source string

const (
	SourceGitLab Source = "gitlab"
	SourceJira   Source = "jira"
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
)

// Actor identifies a source-side user, either as the recipient of an Event
// or as whoever caused it. GitLab has no stable numeric id/email in the
// ingested data, so Username is the match key there; Jira's stable key is
// AccountID (see internal/chunk/jira.Issue.AssigneeAccountID).
type Actor struct {
	Source  Source
	Key     string // GitLab: username. Jira: accountId.
	Display string // human-readable name, for rendering only
}

// Event is a single source-side occurrence addressed to a Recipient.
type Event struct {
	Source     Source
	Type       EventType
	Recipient  Actor // the source-side user this event is FOR
	Actor      Actor // who caused it (assigner); zero value if unknown
	Title      string
	URL        string
	ObjectID   string // stable id of the parent object, e.g. "group/proj!42"
	EventID    string // stable id of this specific event; see dedup key
	OccurredAt time.Time
}

// SelfCaused reports whether the event's recipient is also its actor: a user
// should never be notified about their own action.
func (e Event) SelfCaused() bool {
	return e.Actor.Source != "" && e.Actor.Key != "" && e.Actor == e.Recipient
}

// Notification is a rendered, user-addressed message ready for a Sink.
type Notification struct {
	UserID   uuid.UUID
	Source   Source
	Type     EventType
	Text     string
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
	TelegramUserID     int64
	TelegramAccessHash int64
}

// Sink delivers one Notification to one Target. Implementations must not
// import ent or any gotd/MTProto type, so they stay unit-testable with a
// fake Target.
type Sink interface {
	Channel() Channel
	Deliver(ctx context.Context, target Target, n Notification) error
}
