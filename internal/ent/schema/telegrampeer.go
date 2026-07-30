package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// TelegramPeer is what the bot knows about one Telegram peer: the access hash
// that makes it addressable, plus whatever names it.
//
// It is deliberately not part of User or NotifyChat. An access hash is not a
// fact about a person or a subscription — it is a property of a peer as seen
// by this bot session, it rotates when the session does, and every send path
// needs it (a DM, an alert broadcast, a future proactive message) whether or
// not notifications are involved. Storing it on the notification tables meant
// only someone who had used a notify command had one.
//
// It is filled proactively: the bot records every peer that appears in any
// update it processes, so a peer is addressable before anyone asks for it.
type TelegramPeer struct {
	ent.Schema
}

func (TelegramPeer) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		// "user", "chat" (basic group) or "channel" (channel/supergroup).
		field.String("peer_type"),
		field.Int64("peer_id"),
		// A basic group needs no access hash, so this stays optional.
		field.Int64("access_hash").Optional().Nillable(),
		field.String("username").Optional().Nillable(),
		field.String("title").Optional().Nillable(),
		// last_seen_at answers "is this hash from a live session or from a
		// session that rotated months ago", which is the difference between a
		// send that works and one that fails with PEER_ID_INVALID.
		field.Time("last_seen_at").Default(time.Now).UpdateDefault(time.Now),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (TelegramPeer) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("peer_type", "peer_id").Unique(),
	}
}
