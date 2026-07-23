package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/go-faster/errors"
	"github.com/google/uuid"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/notify"
	"github.com/go-faster/sisyphus/internal/queue"
)

// MaxDeliveryAttempts caps how many times a sink may retry delivering the
// same outbox row before it is left in StatusError for operator inspection,
// instead of retrying forever.
const MaxDeliveryAttempts = 5

// Delivery status values mirrored onto the Notification row.
const (
	StatusPending   = "pending"
	StatusDelivered = "delivered"
	StatusError     = "error"
)

// queueName is the queue backing one channel's deliveries. Channels get their
// own queue so a wedged sink cannot starve another channel's traffic.
func queueName(channel notify.Channel) string { return "notify." + string(channel) }

// payload is what a delivery worker needs to send a message, carried by the
// queue rather than read back from the Notification row. Keeping it
// self-contained is what lets the outbox move to a broker without every
// consumer also needing a Postgres connection.
type payload struct {
	TelegramUserID     int64  `json:"telegram_user_id,omitempty"`
	TelegramAccessHash int64  `json:"telegram_access_hash,omitempty"`
	Text               string `json:"text"`
	URL                string `json:"url,omitempty"`
}

// Enqueue implements notify.OutboxWriter. It writes the Notification row and
// its queue job in one transaction, so work is never queued for a
// notification that does not exist and never lost for one that does.
//
// A repeated dedup key (the collector re-emitted an event already notified)
// is a no-op: created is false and no error is returned.
func (s *Store) Enqueue(ctx context.Context, channel notify.Channel, target notify.Target, n notify.Notification) (bool, error) {
	q, err := s.queue(channel)
	if err != nil {
		return false, err
	}

	tx, err := s.db.Tx(ctx)
	if err != nil {
		return false, errors.Wrap(err, "begin tx")
	}
	defer func() { _ = tx.Rollback() }()

	// The Notification row and its delivery share an ID, so acking a delivery
	// and settling its row address the same identifier.
	id := uuid.New()
	create := tx.Notification.Create().
		SetID(id).
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
	if _, err := create.Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return false, nil
		}
		return false, errors.Wrap(err, "enqueue notification")
	}

	body, err := json.Marshal(payload{
		TelegramUserID:     target.TelegramUserID,
		TelegramAccessHash: target.TelegramAccessHash,
		Text:               n.Text,
		URL:                n.URL,
	})
	if err != nil {
		return false, errors.Wrap(err, "encode delivery payload")
	}
	// The queue key is the row ID, not n.DedupKey: business dedup belongs to
	// the Notification unique index, which was just enforced above. Reusing
	// the dedup key here would add a second, independently-scoped dedup —
	// and since a queue job outlives the row it delivers, a re-enqueue after
	// the old row was cleaned up would be swallowed, leaving a notification
	// that is never delivered.
	if _, err := q.WithTx(tx).Publish(ctx, queue.Message{
		ID:      id,
		Key:     id.String(),
		Payload: body,
	}); err != nil {
		return false, errors.Wrap(err, "publish delivery")
	}

	if err := tx.Commit(); err != nil {
		return false, errors.Wrap(err, "commit")
	}
	return true, nil
}

// OutboxItem is one claimed delivery, as drained by a sink's host process.
type OutboxItem struct {
	ID                 uuid.UUID
	TelegramUserID     int64
	TelegramAccessHash int64
	Text               string
	URL                string
	Attempts           int
}

// Pending claims up to limit deliveries for channel. The claim is leased, so
// two sink processes draining concurrently never deliver the same
// notification twice — and a sink that dies mid-delivery has its work
// reclaimed once the lease lapses, rather than stranding the row.
func (s *Store) Pending(ctx context.Context, channel notify.Channel, limit int) ([]OutboxItem, error) {
	q, err := s.queue(channel)
	if err != nil {
		return nil, err
	}

	batch, err := q.Fetch(ctx, limit)
	if err != nil {
		return nil, errors.Wrap(err, "claim pending notifications")
	}

	out := make([]OutboxItem, 0, len(batch))
	for _, d := range batch {
		var p payload
		if err := json.Unmarshal(d.Payload, &p); err != nil {
			return nil, errors.Wrapf(err, "decode delivery %s", d.ID)
		}
		out = append(out, OutboxItem{
			ID:                 d.ID,
			TelegramUserID:     p.TelegramUserID,
			TelegramAccessHash: p.TelegramAccessHash,
			Text:               p.Text,
			URL:                p.URL,
			Attempts:           d.Attempts,
		})
	}
	return out, nil
}

// Ack records a delivery attempt's outcome against both the queue and the
// Notification row, in one transaction. The queue owns retry scheduling; the
// row mirrors the outcome so operators can query delivery state in the terms
// the rest of the notification system uses.
func (s *Store) Ack(ctx context.Context, id uuid.UUID, deliverErr error) error {
	row, err := s.db.Notification.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return queue.ErrNotFound
		}
		return errors.Wrap(err, "get notification")
	}
	q, err := s.queue(notify.Channel(row.Channel))
	if err != nil {
		return err
	}

	tx, err := s.db.Tx(ctx)
	if err != nil {
		return errors.Wrap(err, "begin tx")
	}
	defer func() { _ = tx.Rollback() }()

	upd := tx.Notification.UpdateOneID(id)
	if deliverErr == nil {
		upd = upd.SetStatus(StatusDelivered).SetDeliveredAt(time.Now())
		if err := q.WithTx(tx).Ack(ctx, id); err != nil {
			return errors.Wrap(err, "ack delivery")
		}
	} else {
		attempts := row.Attempts + 1
		upd = upd.SetAttempts(attempts).SetError(deliverErr.Error())
		if attempts >= MaxDeliveryAttempts {
			upd = upd.SetStatus(StatusError)
		}
		if err := q.WithTx(tx).Nack(ctx, id, deliverErr); err != nil {
			return errors.Wrap(err, "nack delivery")
		}
	}
	if err := upd.Exec(ctx); err != nil {
		return errors.Wrap(err, "settle notification")
	}

	if err := tx.Commit(); err != nil {
		return errors.Wrap(err, "commit")
	}
	return nil
}
