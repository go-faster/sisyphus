package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// UserToken is a long-lived credential issued to a User, for future use by
// consumers that need per-user auth (e.g. MCP access) rather than the
// current single shared bearer token. Only the token's hash is stored,
// never the plaintext; TokenPrefix carries a few leading characters of the
// plaintext so a user can recognize which token is which in a listing
// without the server ever holding the full value. Nothing currently reads
// or writes this table — it's schema-only groundwork; issuing/validating
// tokens and any scope/permission model (RBAC) are deferred.
type UserToken struct {
	ent.Schema
}

func (UserToken) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.UUID("user_id", uuid.UUID{}),
		field.String("name").NotEmpty(),
		field.String("token_hash").NotEmpty().Immutable(),
		field.String("token_prefix").NotEmpty().Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("expires_at").Optional().Nillable(),
		field.Time("last_used_at").Optional().Nillable(),
		field.Time("revoked_at").Optional().Nillable(),
	}
}

func (UserToken) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("tokens").
			Field("user_id").
			Unique().
			Required(),
	}
}

func (UserToken) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("token_hash").Unique(),
		index.Fields("user_id"),
	}
}
