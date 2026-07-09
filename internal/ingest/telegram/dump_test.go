package telegram

import (
	"strings"
	"testing"
	"time"
)

const sampleDump = `{
  "id": -1001234,
  "name": "Support chat",
  "type": "private_group",
  "messages": [
    {
      "id": 1,
      "type": "message",
      "date": "2026-01-15T10:23:45",
      "date_unixtime": "1768465425",
      "from": "Alice",
      "from_id": "user111",
      "text": "invoice stuck",
      "text_entities": [{"type": "plain", "text": "invoice stuck"}]
    },
    {
      "id": 2,
      "type": "message",
      "date": "2026-01-15T10:24:10",
      "date_unixtime": "1768465450",
      "from": "Bob",
      "from_id": "user222",
      "reply_to_message_id": 1,
      "text": [
        "callback ",
        {"type": "bold", "text": "received"}
      ],
      "text_entities": [
        {"type": "plain", "text": "callback "},
        {"type": "bold", "text": "received"}
      ]
    },
    {
      "id": 3,
      "type": "service",
      "date": "2026-01-15T10:25:00",
      "date_unixtime": "1768465500",
      "actor": "Alice",
      "actor_id": "user111",
      "action": "pin_message"
    }
  ]
}`

func TestParseDump(t *testing.T) {
	d, err := ParseDump(strings.NewReader(sampleDump))
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != -1001234 || d.Name != "Support chat" || len(d.Messages) != 3 {
		t.Fatalf("unexpected dump: %+v", d)
	}
}

func TestDumpMessage_PlainText(t *testing.T) {
	d, err := ParseDump(strings.NewReader(sampleDump))
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Messages[0].PlainText(); got != "invoice stuck" {
		t.Fatalf("got %q", got)
	}
	if got := d.Messages[1].PlainText(); got != "callback received" {
		t.Fatalf("got %q", got)
	}
}

func TestDump_RawMessages(t *testing.T) {
	d, err := ParseDump(strings.NewReader(sampleDump))
	if err != nil {
		t.Fatal(err)
	}
	raw := d.RawMessages()
	if len(raw) != 2 {
		t.Fatalf("want 2 raw messages (service message skipped), got %d", len(raw))
	}

	m0 := raw[0]
	if m0.ChatID != -1001234 || m0.MessageID != 1 || m0.SenderID != 111 || m0.SenderName != "Alice" {
		t.Fatalf("unexpected message: %+v", m0)
	}
	if m0.Text != "invoice stuck" {
		t.Fatalf("unexpected text: %q", m0.Text)
	}
	if !m0.Date.Equal(time.Unix(1768465425, 0)) {
		t.Fatalf("unexpected date: %v", m0.Date)
	}
	if len(m0.RawJSON) == 0 {
		t.Fatal("expected non-empty raw json")
	}

	m1 := raw[1]
	if m1.SenderID != 222 || m1.ReplyToID != 1 {
		t.Fatalf("unexpected message: %+v", m1)
	}
	if m1.Text != "callback received" {
		t.Fatalf("unexpected text: %q", m1.Text)
	}
}

func TestDump_RawMessages_MissingFields(t *testing.T) {
	// message.json marks date/date_unixtime/id/text/text_entities/type as
	// required, but exports in the wild may still omit them; RawMessages
	// must not panic and must skip entries with no usable text.
	const sparse = `{"id": 1, "messages": [{"type": "message"}]}`
	d, err := ParseDump(strings.NewReader(sparse))
	if err != nil {
		t.Fatal(err)
	}
	if raw := d.RawMessages(); len(raw) != 0 {
		t.Fatalf("want 0 raw messages for empty text, got %d", len(raw))
	}
}
