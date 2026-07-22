package api

import (
	"context"
	"net/http"

	"github.com/go-faster/errors"
	"github.com/google/uuid"

	"github.com/go-faster/sisyphus/internal/notify"
	notifystore "github.com/go-faster/sisyphus/internal/notify/store"
	"github.com/go-faster/sisyphus/internal/oas"
)

// NotifyStore is the notification persistence Handler needs: enrollment,
// identity linking, and subscription management (called by ssbot's bot
// commands), plus the outbox drain/ack pair (called by ssbot's delivery
// loop). Satisfied by *internal/notify/store.Store.
type NotifyStore interface {
	EnrollTelegram(ctx context.Context, telegramUserID, accessHash int64) (uuid.UUID, error)
	LinkGitLab(ctx context.Context, telegramUserID int64, username string) error
	LinkJira(ctx context.Context, telegramUserID int64, accountID, displayName string) error
	Subscribe(ctx context.Context, telegramUserID int64, source notify.Source, eventTypes []notify.EventType) error
	Unsubscribe(ctx context.Context, telegramUserID int64, source notify.Source) error
	ListSubscriptions(ctx context.Context, telegramUserID int64) ([]notifystore.Subscription, error)
	Pending(ctx context.Context, channel notify.Channel, limit int) ([]notifystore.OutboxItem, error)
	Ack(ctx context.Context, id uuid.UUID, deliverErr error) error
}

// WithNotifyStore sets the notification store, enabling the /notify/* and
// /notifications/* endpoints. Without it, those endpoints return 503: the
// notification system is opt-in (see internal/config.NotifyConfig).
func WithNotifyStore(s NotifyStore) Option {
	return func(h *Handler) {
		h.notify = s
	}
}

// notifyBadRequest maps a caller error (bad identity, unknown user) to a 400
// rather than NewError's default 500, since these are client mistakes, not
// server failures.
func notifyBadRequest(err error) error {
	return &oas.ErrorStatusCode{
		StatusCode: http.StatusBadRequest,
		Response:   oas.Error{ErrorMessage: err.Error()},
	}
}

var errNotifyNotConfigured = &oas.ErrorStatusCode{
	StatusCode: http.StatusServiceUnavailable,
	Response:   oas.Error{ErrorMessage: "notification system not configured"},
}

// NotifyEnroll upserts a NotifyUser's Telegram access hash.
func (h *Handler) NotifyEnroll(ctx context.Context, req *oas.NotifyEnrollRequest) (*oas.Ack, error) {
	if h.notify == nil {
		return nil, errNotifyNotConfigured
	}
	if _, err := h.notify.EnrollTelegram(ctx, req.TelegramUserID, req.TelegramAccessHash); err != nil {
		return nil, errors.Wrap(err, "enroll telegram user")
	}
	return &oas.Ack{Ok: true}, nil
}

// NotifyLink links a Telegram user's GitLab/Jira identity.
func (h *Handler) NotifyLink(ctx context.Context, req *oas.NotifyLinkRequest) (*oas.Ack, error) {
	if h.notify == nil {
		return nil, errNotifyNotConfigured
	}
	var err error
	switch req.Source {
	case oas.NotifyLinkRequestSourceGitlab:
		err = h.notify.LinkGitLab(ctx, req.TelegramUserID, req.Identity)
	case oas.NotifyLinkRequestSourceJira:
		err = h.notify.LinkJira(ctx, req.TelegramUserID, req.Identity, req.DisplayName.Or(""))
	default:
		return nil, notifyBadRequest(errors.Errorf("unknown source %q", req.Source))
	}
	if err != nil {
		return nil, notifyBadRequest(err)
	}
	return &oas.Ack{Ok: true}, nil
}

// NotifySubscribe subscribes a Telegram user to a source's event types.
func (h *Handler) NotifySubscribe(ctx context.Context, req *oas.NotifySubscribeRequest) (*oas.Ack, error) {
	if h.notify == nil {
		return nil, errNotifyNotConfigured
	}
	types := make([]notify.EventType, 0, len(req.EventTypes))
	for _, t := range req.EventTypes {
		types = append(types, notify.EventType(t))
	}
	if err := h.notify.Subscribe(ctx, req.TelegramUserID, notify.Source(req.Source), types); err != nil {
		return nil, notifyBadRequest(err)
	}
	return &oas.Ack{Ok: true}, nil
}

// NotifyUnsubscribe unsubscribes a Telegram user from a source.
func (h *Handler) NotifyUnsubscribe(ctx context.Context, req *oas.NotifyUnsubscribeRequest) (*oas.Ack, error) {
	if h.notify == nil {
		return nil, errNotifyNotConfigured
	}
	if err := h.notify.Unsubscribe(ctx, req.TelegramUserID, notify.Source(req.Source)); err != nil {
		return nil, errors.Wrap(err, "unsubscribe")
	}
	return &oas.Ack{Ok: true}, nil
}

// NotifyListSubscriptions lists a Telegram user's subscriptions.
func (h *Handler) NotifyListSubscriptions(ctx context.Context, params oas.NotifyListSubscriptionsParams) (*oas.NotifySubscriptionsResponse, error) {
	if h.notify == nil {
		return nil, errNotifyNotConfigured
	}
	subs, err := h.notify.ListSubscriptions(ctx, params.TelegramUserID)
	if err != nil {
		return nil, errors.Wrap(err, "list subscriptions")
	}
	out := make([]oas.NotifySubscription, 0, len(subs))
	for _, s := range subs {
		types := make([]string, 0, len(s.EventTypes))
		for _, t := range s.EventTypes {
			types = append(types, string(t))
		}
		out = append(out, oas.NotifySubscription{
			Source:     string(s.Source),
			EventTypes: types,
			Enabled:    s.Enabled,
		})
	}
	return &oas.NotifySubscriptionsResponse{Subscriptions: out}, nil
}

// GetPendingNotifications lists pending Telegram-channel notifications for
// ssbot's delivery loop to drain.
func (h *Handler) GetPendingNotifications(ctx context.Context, params oas.GetPendingNotificationsParams) (*oas.PendingNotificationsResponse, error) {
	if h.notify == nil {
		return nil, errNotifyNotConfigured
	}
	limit := int(params.Limit.Or(20))
	items, err := h.notify.Pending(ctx, notify.ChannelTelegram, limit)
	if err != nil {
		return nil, errors.Wrap(err, "pending notifications")
	}
	out := make([]oas.PendingNotification, 0, len(items))
	for _, it := range items {
		pn := oas.PendingNotification{
			ID:                 it.ID,
			TelegramUserID:     it.TelegramUserID,
			TelegramAccessHash: it.TelegramAccessHash,
			Text:               it.Text,
			Attempts:           it.Attempts,
		}
		if it.URL != "" {
			pn.URL = oas.NewOptString(it.URL)
		}
		out = append(out, pn)
	}
	return &oas.PendingNotificationsResponse{Notifications: out}, nil
}

// AckNotification records a delivery attempt's outcome.
func (h *Handler) AckNotification(ctx context.Context, req *oas.NotificationAckRequest, params oas.AckNotificationParams) (*oas.Ack, error) {
	if h.notify == nil {
		return nil, errNotifyNotConfigured
	}
	var deliverErr error
	if req.Status == oas.NotificationAckRequestStatusError {
		deliverErr = errors.New(req.Error.Or("delivery failed"))
	}
	if err := h.notify.Ack(ctx, params.ID, deliverErr); err != nil {
		return nil, errors.Wrap(err, "ack notification")
	}
	return &oas.Ack{Ok: true}, nil
}
