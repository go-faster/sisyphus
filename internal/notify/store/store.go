// Package store persists the notification system's users, subscriptions,
// and outbox in Postgres via ent. The outbox is a thin domain layer over
// internal/queue (see outbox.go): the queue owns delivery — claims, retries,
// leases — while the Notification row stays the operator-facing record of
// what was sent and what happened to it.
package store

import (
	"context"
	"slices"
	"time"

	"github.com/go-faster/errors"
	"github.com/google/uuid"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/ent/notifysubscription"
	"github.com/go-faster/sisyphus/internal/ent/predicate"
	"github.com/go-faster/sisyphus/internal/ent/user"
	"github.com/go-faster/sisyphus/internal/notify"
	"github.com/go-faster/sisyphus/internal/queue"
)

// Options configures a [Store].
type Options struct {
	// DeliveryLease is how long a claimed delivery is held before another
	// sink may take it. It must exceed a sink's slowest send, or the same
	// notification can go out twice.
	DeliveryLease time.Duration
	// Owner identifies this process in claimed rows, for debugging.
	Owner string
	// Now is the clock, injectable for tests.
	Now func() time.Time
}

func (opts *Options) setDefaults() {
	if opts.DeliveryLease == 0 {
		opts.DeliveryLease = 5 * time.Minute
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
}

// Store persists User/NotifySubscription/Notification rows via ent.
type Store struct {
	db   *ent.Client
	opts Options
}

// New creates a Store backed by db.
func New(db *ent.Client, opts Options) *Store {
	opts.setDefaults()
	return &Store{db: db, opts: opts}
}

// queue returns the delivery queue for channel. Queues are constructed per
// call rather than cached: a [queue.Postgres] is a handle, not a connection.
func (s *Store) queue(channel notify.Channel) (*queue.Postgres, error) {
	if channel == "" {
		return nil, errors.New("empty notify channel")
	}
	return queue.NewPostgres(s.db, queueName(channel), queue.PostgresOptions{
		MaxAttempts: MaxDeliveryAttempts,
		Lease:       s.opts.DeliveryLease,
		Owner:       s.opts.Owner,
		Now:         s.opts.Now,
	}), nil
}

// EnrollTelegram upserts a User row for telegramUserID, persisting its
// current access hash. Called on /subscribe and on every allowlisted
// message from a known user, so a rotated bot session (a new access hash)
// self-heals on the user's next contact rather than requiring re-enrollment.
func (s *Store) EnrollTelegram(ctx context.Context, telegramUserID, accessHash int64) (uuid.UUID, error) {
	err := s.db.User.Create().
		SetTelegramUserID(telegramUserID).
		SetTelegramAccessHash(accessHash).
		SetEnabled(true).
		OnConflictColumns(user.FieldTelegramUserID).
		UpdateNewValues().
		Exec(ctx)
	if err != nil {
		return uuid.Nil, errors.Wrap(err, "enroll telegram user")
	}
	u, err := s.db.User.Query().Where(user.TelegramUserID(telegramUserID)).Only(ctx)
	if err != nil {
		return uuid.Nil, errors.Wrap(err, "get enrolled user")
	}
	return u.ID, nil
}

// LinkGitLab associates telegramUserID with a GitLab username. Returns an
// error if that username is already linked to a different user.
func (s *Store) LinkGitLab(ctx context.Context, telegramUserID int64, username string) error {
	u, err := s.db.User.Query().Where(user.TelegramUserID(telegramUserID)).Only(ctx)
	if err != nil {
		return errors.Wrap(err, "get notify user")
	}
	if err := s.db.User.UpdateOneID(u.ID).SetGitlabUsername(username).Exec(ctx); err != nil {
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
	u, err := s.db.User.Query().Where(user.TelegramUserID(telegramUserID)).Only(ctx)
	if err != nil {
		return errors.Wrap(err, "get notify user")
	}
	upd := s.db.User.UpdateOneID(u.ID).SetJiraAccountID(accountID)
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
	u, err := s.db.User.Query().Where(user.TelegramUserID(telegramUserID)).Only(ctx)
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
	u, err := s.db.User.Query().Where(user.TelegramUserID(telegramUserID)).Only(ctx)
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
	u, err := s.db.User.Query().Where(user.TelegramUserID(telegramUserID)).Only(ctx)
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
	var identityPred predicate.User
	switch source {
	case notify.SourceGitLab:
		if recipient.Key == "" {
			return nil, nil
		}
		identityPred = user.GitlabUsername(recipient.Key)
	case notify.SourceJira:
		if recipient.Key == "" {
			return nil, nil
		}
		identityPred = user.JiraAccountID(recipient.Key)
	default:
		return nil, errors.Errorf("unknown notify source %q", source)
	}

	users, err := s.db.User.Query().
		Where(user.Enabled(true), identityPred).
		All(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "query notify users")
	}
	if len(users) == 0 {
		return nil, nil
	}

	userIDs := make([]uuid.UUID, 0, len(users))
	byID := make(map[uuid.UUID]*ent.User, len(users))
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
