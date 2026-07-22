package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// User is the shared identity table: it links a Telegram identity to the
// GitLab/Jira identities the notification system matches incoming events
// against, and is the anchor point for other per-user concerns as they're
// added (e.g. UserToken for MCP access; RBAC roles/permissions, deferred).
type User struct {
	ent.Schema
}

func (User) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.Int64("telegram_user_id"),
		field.Int64("telegram_access_hash").Optional().Nillable(),
		field.String("gitlab_username").Optional().Nillable(),
		field.String("jira_account_id").Optional().Nillable(),
		field.String("jira_display_name").Optional().Nillable(),
		field.Bool("enabled").Default(true),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (User) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("subscriptions", NotifySubscription.Type),
		edge.To("notifications", Notification.Type),
		edge.To("tokens", UserToken.Type),
	}
}

func (User) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("telegram_user_id").Unique(),
		index.Fields("gitlab_username").Unique(),
		index.Fields("jira_account_id").Unique(),
	}
}
