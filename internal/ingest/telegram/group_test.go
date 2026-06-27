package telegram

import (
	"testing"
	"time"
)

func ts(minutes int) time.Time {
	return time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC).Add(time.Duration(minutes) * time.Minute)
}

func TestGroup(t *testing.T) {
	t.Run("time window splits conversations", func(t *testing.T) {
		msgs := []Message{
			{ChatID: 1, MessageID: 1, Text: "hi", Date: ts(0)},
			{ChatID: 1, MessageID: 2, Text: "invoice stuck", Date: ts(5)},
			// Big gap -> new conversation.
			{ChatID: 1, MessageID: 3, Text: "unrelated", Date: ts(120)},
		}
		got := Group(msgs, GroupOptions{Window: 45 * time.Minute})
		if len(got) != 2 {
			t.Fatalf("want 2 conversations, got %d", len(got))
		}
		if len(got[0].Messages) != 2 || got[0].FirstMessageID != 1 || got[0].LastMessageID != 2 {
			t.Fatalf("unexpected first conversation: %+v", got[0])
		}
		if got[1].FirstMessageID != 3 {
			t.Fatalf("unexpected second conversation: %+v", got[1])
		}
	})

	t.Run("reply attaches across the window", func(t *testing.T) {
		msgs := []Message{
			{ChatID: 1, MessageID: 1, Text: "root", Date: ts(0)},
			{ChatID: 1, MessageID: 2, Text: "filler", Date: ts(10)},
			// Far in time, but replies to message 1 -> same conversation.
			{ChatID: 1, MessageID: 3, Text: "reply to root", Date: ts(200), ReplyToID: 1},
		}
		got := Group(msgs, GroupOptions{Window: 45 * time.Minute})
		if len(got) != 1 {
			t.Fatalf("want 1 conversation (reply chain), got %d", len(got))
		}
		if len(got[0].Messages) != 3 {
			t.Fatalf("want 3 messages, got %d", len(got[0].Messages))
		}
	})

	t.Run("unsorted input is ordered", func(t *testing.T) {
		msgs := []Message{
			{ChatID: 1, MessageID: 2, Text: "second", Date: ts(5)},
			{ChatID: 1, MessageID: 1, Text: "first", Date: ts(0)},
		}
		got := Group(msgs, GroupOptions{})
		if len(got) != 1 || got[0].Messages[0].MessageID != 1 {
			t.Fatalf("expected ordered single conversation, got %+v", got)
		}
	})

	t.Run("MinMessages drops noise", func(t *testing.T) {
		msgs := []Message{
			{ChatID: 1, MessageID: 1, Text: "lonely", Date: ts(0)},
			{ChatID: 1, MessageID: 2, Text: "a", Date: ts(120)},
			{ChatID: 1, MessageID: 3, Text: "b", Date: ts(121)},
		}
		got := Group(msgs, GroupOptions{Window: 45 * time.Minute, MinMessages: 2})
		if len(got) != 1 {
			t.Fatalf("want 1 conversation after dropping singletons, got %d", len(got))
		}
	})

	t.Run("empty", func(t *testing.T) {
		if got := Group(nil, GroupOptions{}); got != nil {
			t.Fatalf("want nil, got %+v", got)
		}
	})
}

func TestConversationRawText(t *testing.T) {
	c := Conversation{Messages: []Message{
		{SenderName: "alice", Text: "invoice stuck"},
		{Text: "  callback received  "},
	}}
	want := "alice: invoice stuck\nuser: callback received"
	if got := c.RawText(); got != want {
		t.Fatalf("RawText()=%q want %q", got, want)
	}
}
