package telegram

import (
	"fmt"
	"time"

	"github.com/go-faster/scpbot/internal/index"
)

// DocumentFromConversation builds a normalized Document for a grouped support
// conversation (plan §4). summary may be empty (LLM deferred); the raw text is
// always preserved so we can reindex. messageIDs is recorded in metadata.
func DocumentFromConversation(c Conversation, summary, service string) index.Document {
	raw := c.RawText()
	ids := make([]int64, 0, len(c.Messages))
	for _, m := range c.Messages {
		ids = append(ids, m.MessageID)
	}
	var created, updated time.Time
	if len(c.Messages) > 0 {
		created = c.Messages[0].Date
		updated = c.Messages[len(c.Messages)-1].Date
	}

	meta := map[string]any{
		"source":      string(index.SourceTelegram),
		"chat_id":     c.ChatID,
		"message_ids": ids,
		"status":      "new",
		"authority":   string(index.AuthorityLow), // raw discussion is low authority
	}
	if summary != "" {
		meta["summary"] = summary
	}
	if service != "" {
		meta["service"] = service
	}

	return index.Document{
		ID:        index.NewID(),
		Source:    index.SourceTelegram,
		SourceID:  fmt.Sprintf("%d:%d", c.ChatID, c.FirstMessageID),
		Title:     fmt.Sprintf("Telegram support request (chat %d)", c.ChatID),
		Body:      raw,
		BodyHash:  index.Hash(raw),
		Metadata:  meta,
		CreatedAt: created,
		UpdatedAt: updated,
	}
}
