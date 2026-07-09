package telegram

import (
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/go-faster/errors"
)

// Dump is one Telegram Desktop / GDPR chat export JSON file: a single chat's
// id/name/type plus its messages (dump.json schema). Every field mirrors the
// export format 1:1; none are required to be present.
type Dump struct {
	ID       int64         `json:"id"`
	Name     string        `json:"name"`
	Type     string        `json:"type"`
	Messages []DumpMessage `json:"messages"`
}

// DumpTextEntity is one formatted span of a message's text_entities array
// (message.json schema). Only Text is used; Type/Href/etc. are ignored since
// we index plain text.
type DumpTextEntity struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// DumpMessage is one entry of Dump.Messages (message.json schema). Every
// field is optional: exports vary by message kind (text, media, service,
// poll, ...) and only Type/Date/ID/Text/TextEntities are commonly present.
type DumpMessage struct {
	ID               int64  `json:"id"`
	Type             string `json:"type"` // "message" or "service"
	Date             string `json:"date"`
	DateUnixtime     string `json:"date_unixtime"`
	From             string `json:"from"`
	FromID           string `json:"from_id"`
	Actor            string `json:"actor"`
	ActorID          string `json:"actor_id"`
	Action           string `json:"action"`
	ReplyToMessageID int64  `json:"reply_to_message_id"`
	ForwardedFrom    string `json:"forwarded_from"`
	MediaType        string `json:"media_type"`
	// Text is either a bare string or an array mixing bare strings and
	// {text,type,...} objects; parse it via PlainText, not directly.
	Text         json.RawMessage  `json:"text"`
	TextEntities []DumpTextEntity `json:"text_entities"`
}

// ParseDump decodes one Telegram chat export file.
func ParseDump(r io.Reader) (Dump, error) {
	var d Dump
	if err := json.NewDecoder(r).Decode(&d); err != nil {
		return Dump{}, errors.Wrap(err, "decode telegram dump")
	}
	return d, nil
}

// PlainText flattens a message's text into plain text, preferring the
// structured text_entities (always plain strings) and falling back to the
// raw text field.
func (m DumpMessage) PlainText() string {
	if len(m.TextEntities) > 0 {
		var sb strings.Builder
		for _, e := range m.TextEntities {
			sb.WriteString(e.Text)
		}
		return sb.String()
	}
	return parseDumpText(m.Text)
}

// parseDumpText handles the export format's "text" field, which is either a
// bare string or an array mixing bare strings and {text,type,...} objects.
func parseDumpText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return ""
	}
	var sb strings.Builder
	for _, item := range items {
		var s string
		if err := json.Unmarshal(item, &s); err == nil {
			sb.WriteString(s)
			continue
		}
		var obj struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(item, &obj); err == nil {
			sb.WriteString(obj.Text)
		}
	}
	return sb.String()
}

// dumpSenderID extracts the numeric suffix from the export's "user123" /
// "channel123" / "chat123" from_id form. Non-numeric or empty input yields 0.
func dumpSenderID(id string) int64 {
	id = strings.TrimPrefix(id, "user")
	id = strings.TrimPrefix(id, "channel")
	id = strings.TrimPrefix(id, "chat")
	n, _ := strconv.ParseInt(id, 10, 64)
	return n
}

// dumpDate parses date_unixtime (preferred, unambiguous) or falls back to
// the "date" field's local-time layout used by the export format.
func dumpDate(m DumpMessage) time.Time {
	if m.DateUnixtime != "" {
		if secs, err := strconv.ParseInt(m.DateUnixtime, 10, 64); err == nil {
			return time.Unix(secs, 0)
		}
	}
	if m.Date != "" {
		if t, err := time.Parse("2006-01-02T15:04:05", m.Date); err == nil {
			return t
		}
	}
	return time.Time{}
}

// RawMessages converts a Dump's message entries into RawMessage, skipping
// service messages (joins/leaves/pins/...) and empty-text entries since
// neither carries support content.
func (d Dump) RawMessages() []RawMessage {
	out := make([]RawMessage, 0, len(d.Messages))
	for _, m := range d.Messages {
		if m.Type != "message" {
			continue
		}
		text := m.PlainText()
		if text == "" {
			continue
		}

		data, _ := json.Marshal(m)
		out = append(out, RawMessage{
			ChatID:     d.ID,
			MessageID:  int(m.ID),
			SenderID:   dumpSenderID(m.FromID),
			SenderName: m.From,
			Text:       text,
			Date:       dumpDate(m),
			ReplyToID:  int(m.ReplyToMessageID),
			RawJSON:    data,
		})
	}
	return out
}
