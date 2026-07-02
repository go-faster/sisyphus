package telegram

import (
	"context"
	"encoding/json"

	"github.com/go-faster/errors"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/ent/telegrammessage"
)

const persistBatchSize = 500

// persistMessages upserts RawMessages into telegram_messages. Empty raw_json
// is stored as the default empty JSON object.
func persistMessages(ctx context.Context, db *ent.Client, msgs []RawMessage) error {
	for i := 0; i < len(msgs); i += persistBatchSize {
		end := min(i+persistBatchSize, len(msgs))
		batch := msgs[i:end]

		builders := make([]*ent.TelegramMessageCreate, len(batch))
		for j, m := range batch {
			b := db.TelegramMessage.Create().
				SetChatID(m.ChatID).
				SetMessageID(int64(m.MessageID)).
				SetMessageDate(m.Date)

			if m.Text != "" {
				b.SetText(m.Text)
			}
			if m.SenderID != 0 {
				b.SetSenderID(m.SenderID)
			}
			if m.SenderName != "" {
				b.SetSenderName(m.SenderName)
			}
			if m.ReplyToID != 0 {
				b.SetReplyToID(int64(m.ReplyToID))
			}
			if m.ThreadID != 0 {
				b.SetThreadID(int64(m.ThreadID))
			}

			rawJSON := m.RawJSON
			if rawJSON == nil {
				rawJSON = []byte("{}")
			}
			var jsonMap map[string]any
			if err := json.Unmarshal(rawJSON, &jsonMap); err != nil {
				jsonMap = map[string]any{}
			}
			b.SetRawJSON(jsonMap)

			builders[j] = b
		}

		if err := db.TelegramMessage.CreateBulk(builders...).
			OnConflictColumns(telegrammessage.FieldChatID, telegrammessage.FieldMessageID).
			UpdateNewValues().
			Exec(ctx); err != nil {
			return errors.Wrap(err, "persist messages batch")
		}
	}
	return nil
}

// persistSupportRequests upserts Conversations as support_requests.
func persistSupportRequests(ctx context.Context, db *ent.Client, chatID int64, convs []Conversation) error {
	for i := 0; i < len(convs); i += persistBatchSize {
		end := min(i+persistBatchSize, len(convs))
		batch := convs[i:end]

		builders := make([]*ent.SupportRequestCreate, len(batch))
		for j, c := range batch {
			rawText := c.RawText()

			b := db.SupportRequest.Create().
				SetChatID(chatID).
				SetFirstMessageID(c.FirstMessageID).
				SetLastMessageID(c.LastMessageID).
				SetRawText(rawText).
				SetStatus("new")

			builders[j] = b
		}

		if err := db.SupportRequest.CreateBulk(builders...).
			OnConflictColumns("chat_id", "first_message_id").
			UpdateNewValues().
			Exec(ctx); err != nil {
			return errors.Wrap(err, "persist support requests batch")
		}
	}
	return nil
}
