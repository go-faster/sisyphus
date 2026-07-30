package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// NotifyChat is a chat registered to receive broadcast notifications — today
// an agent's investigation of a firing alert.
//
// It exists because a broadcast has no subscribed user to address: the report
// is for whoever watches the channel. The row is written by the bot when
// someone runs /alerts on *inside* the chat, which is also the only place the
// peer's access hash can be obtained without hand-copying it into config.
type NotifyChat struct {
	ent.Schema
}

func (NotifyChat) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		// peer_type/peer_id/access_hash are the MTProto peer, captured from
		// the update that registered it. A basic group needs no access hash.
		field.String("peer_type").Default("channel"),
		field.Int64("peer_id"),
		field.Int64("access_hash").Optional().Nillable(),
		field.String("title").Optional(),
		// enabled is how /alerts off works: the row is kept so the access
		// hash survives, and a later /alerts on does not need the chat to be
		// re-resolved.
		field.Bool("enabled").Default(true),
		// added_by is the Telegram user who registered the chat, for audit.
		field.Int64("added_by").Optional(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (NotifyChat) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("peer_type", "peer_id").Unique(),
		index.Fields("enabled"),
	}
}
