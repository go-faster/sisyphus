// Package store persists the notification system's users, subscriptions,
// and outbox in Postgres via ent, mirroring internal/agentstore's
// create-or-get idempotency pattern for the outbox (Enqueue) and its
// status-lifecycle pattern for delivery (Pending/Ack).
package store

import (
	"context"
	"slices"
	"time"

	"github.com/go-faster/errors"
	"github.com/google/uuid"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/ent/notification"
	"github.com/go-faster/sisyphus/internal/ent/notifysubscription"
	"github.com/go-faster/sisyphus/internal/ent/notifyuser"
	"github.com/go-faster/sisyphus/internal/ent/predicate"
	"github.com/go-faster/sisyphus/internal/notify"
)

// MaxDeliveryAttempts caps how many times ssbot may retry delivering the
// same outbox row before Ack gives up and leaves it in StatusError for
// operator inspection, instead of retrying forever.
const MaxDeliveryAttempts = 5

// Delivery status values for the Notification outbox's status column.
const (
	StatusPending   = "pending"
	StatusDelivered = "delivered"
	StatusError     = "error"
)

// Store persists NotifyUser/NotifySubscription/Notification rows via ent.
type Store struct {
	db *ent.Client
}

// New creates a Store backed by db.
func New(db *ent.Client) *Store {
	return &Store{db: db}
}

// EnrollTelegram upserts a NotifyUser for telegramUserID, persisting its
// current access hash. Called on /subscribe and on every allowlisted
// message from a known user, so a rotated bot session (a new access hash)
// self-heals on the user's next contact rather than requiring re-enrollment.
func (s *Store) EnrollTelegram(ctx context.Context, telegramUserID, accessHash int64) (uuid.UUID, error) {
	err := s.db.NotifyUser.Create().
		SetTelegramUserID(telegramUserID).
		SetTelegramAccessHash(accessHash).
		SetEnabled(true).
		OnConflictColumns(notifyuser.FieldTelegramUserID).
		UpdateNewValues().
		Exec(ctx)
	if err != nil {
		return uuid.Nil, errors.Wrap(err, "enroll telegram user")
	}
	u, err := s.db.NotifyUser.Query().Where(notifyuser.TelegramUserID(telegramUserID)).Only(ctx)
	if err != nil {
		return uuid.Nil, errors.Wrap(err, "get enrolled user")
	}
	return u.ID, nil
}

// LinkGitLab associates telegramUserID with a GitLab username. Returns an
// error if that username is already linked to a different user.
func (s *Store) LinkGitLab(ctx context.Context, telegramUserID int64, username string) error {
	u, err := s.db.NotifyUser.Query().Where(notifyuser.TelegramUserID(telegramUserID)).Only(ctx)
	if err != nil {
		return errors.Wrap(err, "get notify user")
	}
	if err := s.db.NotifyUser.UpdateOneID(u.ID).SetGitlabUsername(username).Exec(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return errors.Errorf("gitlab username %q is already linked to another user", username)
		}
		return errors.Wrap(err, "link gitlab username")
	}
	return nil
}

// LinkJira associates telegramUserID with a Jira accountId. Returns an error
// if that accountId is already linked to a different user.
func (s *Store) LinkJira(ctx context.Context, telegramUserID int64, accountID, displayName string) error {
	u, err := s.db.NotifyUser.Query().Where(notifyuser.TelegramUserID(telegramUserID)).Only(ctx)
	if err != nil {
		return errors.Wrap(err, "get notify user")
	}
	upd := s.db.NotifyUser.UpdateOneID(u.ID).SetJiraAccountID(accountID)
	if displayName != "" {
		upd = upd.SetJiraDisplayName(displayName)
	}
	if err := upd.Exec(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return errors.Errorf("jira account %q is already linked to another user", accountID)
		}
		return errors.Wrap(err, "link jira account")
	}
	return nil
}

// Subscribe upserts telegramUserID's subscription to source, replacing its
// event type list. Calling it again with a different eventTypes list updates
// the subscription in place rather than creating a second row (unique on
// (user_id, source)).
func (s *Store) Subscribe(ctx context.Context, telegramUserID int64, source notify.Source, eventTypes []notify.EventType) error {
	u, err := s.db.NotifyUser.Query().Where(notifyuser.TelegramUserID(telegramUserID)).Only(ctx)
	if err != nil {
		return errors.Wrap(err, "get notify user")
	}

	types := make([]string, 0, len(eventTypes))
	for _, t := range eventTypes {
		types = append(types, string(t))
	}

	err = s.db.NotifySubscription.Create().
		SetUserID(u.ID).
		SetSource(string(source)).
		SetEventTypes(types).
		SetEnabled(true).
		OnConflictColumns(notifysubscription.FieldUserID, notifysubscription.FieldSource).
		UpdateNewValues().
		Exec(ctx)
	if err != nil {
		return errors.Wrap(err, "subscribe")
	}
	return nil
}

// Unsubscribe disables telegramUserID's subscription to source, if any.
func (s *Store) Unsubscribe(ctx context.Context, telegramUserID int64, source notify.Source) error {
	u, err := s.db.NotifyUser.Query().Where(notifyuser.TelegramUserID(telegramUserID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil
		}
		return errors.Wrap(err, "get notify user")
	}

	_, err = s.db.NotifySubscription.Update().
		Where(
			notifysubscription.UserID(u.ID),
			notifysubscription.Source(string(source)),
		).
		SetEnabled(false).
		Save(ctx)
	if err != nil {
		return errors.Wrap(err, "unsubscribe")
	}
	return nil
}

// Subscription describes one of a user's subscriptions, for the
// /notifications listing command.
type Subscription struct {
	Source     notify.Source
	EventTypes []notify.EventType
	Enabled    bool
}

// ListSubscriptions returns telegramUserID's subscriptions.
func (s *Store) ListSubscriptions(ctx context.Context, telegramUserID int64) ([]Subscription, error) {
	u, err := s.db.NotifyUser.Query().Where(notifyuser.TelegramUserID(telegramUserID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, errors.Wrap(err, "get notify user")
	}

	rows, err := s.db.NotifySubscription.Query().Where(notifysubscription.UserID(u.ID)).All(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "list subscriptions")
	}

	out := make([]Subscription, 0, len(rows))
	for _, r := range rows {
		types := make([]notify.EventType, 0, len(r.EventTypes))
		for _, t := range r.EventTypes {
			types = append(types, notify.EventType(t))
		}
		out = append(out, Subscription{
			Source:     notify.Source(r.Source),
			EventTypes: types,
			Enabled:    r.Enabled,
		})
	}
	return out, nil
}

// Subscribers implements notify.SubscriptionLookup: it finds enabled
// subscriptions to (source, eventType) belonging to the user whose linked
// identity matches recipient.
func (s *Store) Subscribers(ctx context.Context, source notify.Source, eventType notify.EventType, recipient notify.Actor) ([]notify.Subscriber, error) {
	var identityPred predicate.NotifyUser
	switch source {
	case notify.SourceGitLab:
		if recipient.Key == "" {
			return nil, nil
		}
		identityPred = notifyuser.GitlabUsername(recipient.Key)
	case notify.SourceJira:
		if recipient.Key == "" {
			return nil, nil
		}
		identityPred = notifyuser.JiraAccountID(recipient.Key)
	default:
		return nil, errors.Errorf("unknown notify source %q", source)
	}

	users, err := s.db.NotifyUser.Query().
		Where(notifyuser.Enabled(true), identityPred).
		All(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "query notify users")
	}
	if len(users) == 0 {
		return nil, nil
	}

	userIDs := make([]uuid.UUID, 0, len(users))
	byID := make(map[uuid.UUID]*ent.NotifyUser, len(users))
	for _, u := range users {
		userIDs = append(userIDs, u.ID)
		byID[u.ID] = u
	}

	subs, err := s.db.NotifySubscription.Query().
		Where(
			notifysubscription.UserIDIn(userIDs...),
			notifysubscription.Source(string(source)),
			notifysubscription.Enabled(true),
		).
		All(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "query subscriptions")
	}

	var out []notify.Subscriber
	for _, sub := range subs {
		if !containsEventType(sub.EventTypes, eventType) {
			continue
		}
		u := byID[sub.UserID]
		if u == nil || u.TelegramAccessHash == nil {
			continue
		}
		out = append(out, notify.Subscriber{
			UserID: u.ID,
			Target: notify.Target{
				TelegramUserID:     u.TelegramUserID,
				TelegramAccessHash: *u.TelegramAccessHash,
			},
		})
	}
	return out, nil
}

func containsEventType(types []string, want notify.EventType) bool {
	return slices.Contains(types, string(want))
}

// Enqueue implements notify.OutboxWriter. It writes a pending outbox row for
// n.DedupKey; a repeated dedup key (the collector re-emitted an event
// already notified) is a no-op — created is false and no error is returned,
// matching internal/agentstore.Store.Submit's idempotency-key pattern.
func (s *Store) Enqueue(ctx context.Context, channel notify.Channel, target notify.Target, n notify.Notification) (bool, error) {
	create := s.db.Notification.Create().
		SetDedupKey(n.DedupKey).
		SetUserID(n.UserID).
		SetChannel(string(channel)).
		SetSource(string(n.Source)).
		SetEventType(string(n.Type)).
		SetText(n.Text).
		SetURL(n.URL)
	if channel == notify.ChannelTelegram {
		create = create.
			SetTelegramUserID(target.TelegramUserID).
			SetTelegramAccessHash(target.TelegramAccessHash)
	}

	_, err := create.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return false, nil
		}
		return false, errors.Wrap(err, "enqueue notification")
	}
	return true, nil
}

// OutboxItem is one pending delivery, as drained by a sink's host process.
type OutboxItem struct {
	ID                 uuid.UUID
	TelegramUserID     int64
	TelegramAccessHash int64
	Text               string
	URL                string
	Attempts           int
}

// Pending returns up to limit pending outbox rows for channel, oldest first.
func (s *Store) Pending(ctx context.Context, channel notify.Channel, limit int) ([]OutboxItem, error) {
	rows, err := s.db.Notification.Query().
		Where(
			notification.Channel(string(channel)),
			notification.Status(StatusPending),
		).
		Order(notification.ByCreatedAt()).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "query pending notifications")
	}

	out := make([]OutboxItem, 0, len(rows))
	for _, r := range rows {
		var hash int64
		if r.TelegramAccessHash != nil {
			hash = *r.TelegramAccessHash
		}
		out = append(out, OutboxItem{
			ID:                 r.ID,
			TelegramUserID:     r.TelegramUserID,
			TelegramAccessHash: hash,
			Text:               r.Text,
			URL:                r.URL,
			Attempts:           r.Attempts,
		})
	}
	return out, nil
}

// Ack records a delivery attempt's outcome. A successful delivery transitions
// the row to StatusDelivered. A failure increments Attempts and, once
// MaxDeliveryAttempts is reached, transitions to StatusError instead of
// leaving the row pending forever for ssbot to keep retrying.
func (s *Store) Ack(ctx context.Context, id uuid.UUID, deliverErr error) error {
	if deliverErr == nil {
		if err := s.db.Notification.UpdateOneID(id).
			SetStatus(StatusDelivered).
			SetDeliveredAt(time.Now()).
			Exec(ctx); err != nil {
			return errors.Wrap(err, "ack delivered notification")
		}
		return nil
	}

	row, err := s.db.Notification.Get(ctx, id)
	if err != nil {
		return errors.Wrap(err, "get notification")
	}

	upd := row.Update().
		SetAttempts(row.Attempts + 1).
		SetError(deliverErr.Error())
	if row.Attempts+1 >= MaxDeliveryAttempts {
		upd = upd.SetStatus(StatusError)
	}
	if err := upd.Exec(ctx); err != nil {
		return errors.Wrap(err, "ack failed notification")
	}
	return nil
}
