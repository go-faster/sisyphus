package telegram

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gotd/td/tg"
)

func TestConvertTGMessage_PlainText(t *testing.T) {
	msg := &tg.Message{
		ID:      123,
		Message: "hello world",
		Date:    1700000000,
	}
	raw := convertTGMessage(context.Background(), -100123, msg)

	if raw.ChatID != -100123 {
		t.Fatalf("want ChatID=-100123, got %d", raw.ChatID)
	}
	if raw.MessageID != 123 {
		t.Fatalf("want MessageID=123, got %d", raw.MessageID)
	}
	if raw.Text != "hello world" {
		t.Fatalf("want Text=hello world, got %q", raw.Text)
	}
	if len(raw.RawJSON) == 0 {
		t.Fatal("expected RawJSON non-empty")
	}
	var m map[string]any
	if err := json.Unmarshal(raw.RawJSON, &m); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}
	if m["Message"] != "hello world" {
		t.Fatalf("want Message=hello world in JSON, got %v", m["Message"])
	}
}

func TestConvertTGMessage_SenderPeerUser(t *testing.T) {
	msg := &tg.Message{
		ID:      1,
		Message: "x",
		Date:    0,
		FromID:  &tg.PeerUser{UserID: 42},
	}
	raw := convertTGMessage(context.Background(), 100, msg)
	if raw.SenderID != 42 {
		t.Fatalf("want SenderID=42, got %d", raw.SenderID)
	}
}

func TestConvertTGMessage_ReplyTo(t *testing.T) {
	h := &tg.MessageReplyHeader{}
	h.SetReplyToMsgID(99)
	msg := &tg.Message{
		ID:      10,
		Message: "reply",
		Date:    0,
		ReplyTo: h,
	}
	raw := convertTGMessage(context.Background(), 1, msg)
	if raw.ReplyToID != 99 {
		t.Fatalf("want ReplyToID=99, got %d", raw.ReplyToID)
	}
}

func TestConvertTGMessage_Date(t *testing.T) {
	msg := &tg.Message{
		ID:      1,
		Message: "d",
		Date:    1700000000,
	}
	raw := convertTGMessage(context.Background(), 1, msg)
	want := time.Unix(1700000000, 0)
	if !raw.Date.Equal(want) {
		t.Fatalf("want Date=%v, got %v", want, raw.Date)
	}
}

func TestConvertTGMessage_NilLoggerNoPanic(t *testing.T) {
	msg := &tg.Message{
		ID:      1,
		Message: "safe",
		Date:    0,
	}
	// Should not panic even with nil logger.
	raw := convertTGMessage(context.Background(), 1, msg)
	if raw.Text != "safe" {
		t.Fatalf("want Text=safe, got %q", raw.Text)
	}
	if len(raw.RawJSON) == 0 {
		t.Fatal("expected RawJSON to be populated")
	}
}
