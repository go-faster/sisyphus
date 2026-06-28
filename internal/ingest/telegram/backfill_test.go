package telegram

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/go-faster/scpbot/internal/ent"
	"github.com/go-faster/scpbot/internal/index"
)

// fakeFetcher implements MessageFetcher with canned data for tests.
type fakeFetcher struct {
	pages [][]RawMessage // each call returns the next page; last page hasMore=false
	call  int
}

func (f *fakeFetcher) FetchHistory(_ context.Context, chatID int64, beforeMsgID, limit int) ([]RawMessage, bool, error) {
	if f.call >= len(f.pages) {
		return nil, false, nil
	}
	page := f.pages[f.call]
	f.call++

	hasMore := f.call < len(f.pages)
	if len(page) > limit {
		page = page[:limit]
	}
	return page, hasMore, nil
}

func TestNewBackfiller_NoSession(t *testing.T) {
	_, err := NewBackfiller(nil, BackfillOptions{})
	if err == nil {
		t.Fatal("expected error for nil session, got nil")
	}
}

func TestCursorEncodeDecode(t *testing.T) {
	c := Cursor{PerChat: map[int64]int{100: 42, 200: 99}}
	s, err := c.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeCursor(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.PerChat) != 2 {
		t.Fatalf("want 2 entries, got %d", len(got.PerChat))
	}
	if got.PerChat[100] != 42 {
		t.Fatalf("want per_chat[100]=42, got %d", got.PerChat[100])
	}
	if got.PerChat[200] != 99 {
		t.Fatalf("want per_chat[200]=99, got %d", got.PerChat[200])
	}
}

func TestCursorEmpty(t *testing.T) {
	s, err := Cursor{}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeCursor(s)
	if err != nil {
		t.Fatal(err)
	}
	if got.PerChat == nil {
		t.Fatal("expected non-nil PerChat map")
	}
	if len(got.PerChat) != 0 {
		t.Fatalf("expected empty PerChat, got %d entries", len(got.PerChat))
	}
}

func TestRawMessageToMessage(t *testing.T) {
	now := time.Now()
	raw := RawMessage{
		ChatID:     -100123,
		MessageID:  42,
		SenderID:   100500,
		SenderName: "Alice",
		Text:       "hello",
		Date:       now,
		ReplyToID:  41,
	}
	msgs := rawMessagesToMessages([]RawMessage{raw})
	if len(msgs) != 1 {
		t.Fatalf("want 1 message, got %d", len(msgs))
	}
	m := msgs[0]
	if m.ChatID != -100123 || m.MessageID != 42 || m.SenderID != 100500 {
		t.Fatalf("unexpected message fields: %+v", m)
	}
	if m.SenderName != "Alice" || m.Text != "hello" || m.ReplyToID != 41 {
		t.Fatalf("unexpected message fields: %+v", m)
	}
	if !m.Date.Equal(now) {
		t.Fatal("date mismatch")
	}
}

func TestBackfill_SingleChatTwoPages(t *testing.T) {
	db := testDB(t)
	if db == nil {
		t.Skip("set SCPBOT_TEST_DB to run ent-backed tests")
	}

	chatID := int64(-100123)
	page1 := make([]RawMessage, 100)
	for i := range 100 {
		page1[i] = RawMessage{
			ChatID:    chatID,
			MessageID: 200 - i,
			Date:      time.Date(2026, 6, 1, 10, 0, i, 0, time.UTC),
			Text:      "msg",
			SenderID:  1,
		}
	}
	page2 := make([]RawMessage, 50)
	for i := range 50 {
		page2[i] = RawMessage{
			ChatID:    chatID,
			MessageID: 100 - i,
			Date:      time.Date(2026, 6, 1, 10, 0, 100+i, 0, time.UTC),
			Text:      "msg",
			SenderID:  1,
		}
	}

	fetcher := &fakeFetcher{pages: [][]RawMessage{page1, page2}}
	b, err := NewBackfiller(db, BackfillOptions{Fetcher: fetcher})
	if err != nil {
		t.Fatal(err)
	}

	result, err := b.Backfill(context.Background(), BackfillRequest{
		Chats: []ChatSpec{{ID: chatID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.TotalMessages != 150 {
		t.Fatalf("want 150 messages, got %d", result.TotalMessages)
	}
	if result.TotalConvos == 0 {
		t.Fatal("expected at least 1 conversation")
	}

	// Verify cursor points to newest message ID.
	if result.NextCursor.PerChat[chatID] != 200 {
		t.Fatalf("want cursor=200, got %d", result.NextCursor.PerChat[chatID])
	}

	// Verify documents have correct Source.
	for _, doc := range result.Documents {
		if doc.Source != index.SourceTelegram {
			t.Fatalf("unexpected source %q", doc.Source)
		}
	}

	// Verify messages were persisted (idempotent test in another case).
	count, err := db.TelegramMessage.Query().Count(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 150 {
		t.Fatalf("want 150 persisted messages, got %d", count)
	}
}

func TestBackfill_MultiChat(t *testing.T) {
	db := testDB(t)
	if db == nil {
		t.Skip("set SCPBOT_TEST_DB to run ent-backed tests")
	}

	fetcher := &fakeFetcher{
		pages: [][]RawMessage{
			{
				{ChatID: -1001, MessageID: 1, Date: time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC), Text: "a", SenderID: 1},
				{ChatID: -1001, MessageID: 2, Date: time.Date(2026, 6, 1, 10, 5, 0, 0, time.UTC), Text: "b", SenderID: 1},
			},
			{
				{ChatID: -1002, MessageID: 10, Date: time.Date(2026, 6, 1, 11, 0, 0, 0, time.UTC), Text: "c", SenderID: 2},
			},
		},
	}

	b, err := NewBackfiller(db, BackfillOptions{Fetcher: fetcher})
	if err != nil {
		t.Fatal(err)
	}

	result, err := b.Backfill(context.Background(), BackfillRequest{
		Chats: []ChatSpec{
			{ID: -1001},
			{ID: -1002},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.TotalMessages != 3 {
		t.Fatalf("want 3 messages, got %d", result.TotalMessages)
	}

	// Both chats should have cursor entries.
	if _, ok := result.NextCursor.PerChat[-1001]; !ok {
		t.Fatal("missing cursor for chat -1001")
	}
	if _, ok := result.NextCursor.PerChat[-1002]; !ok {
		t.Fatal("missing cursor for chat -1002")
	}
}

func TestBackfill_CursorResume(t *testing.T) {
	db := testDB(t)
	if db == nil {
		t.Skip("set SCPBOT_TEST_DB to run ent-backed tests")
	}

	chatID := int64(-100123)
	calledWith := 0
	fetcher := &callTrackingFetcher{
		fn: func(_ context.Context, chatID int64, beforeMsgID int, limit int) ([]RawMessage, bool, error) {
			calledWith = beforeMsgID
			return nil, false, nil
		},
	}

	b, err := NewBackfiller(db, BackfillOptions{Fetcher: fetcher})
	if err != nil {
		t.Fatal(err)
	}

	_, err = b.Backfill(context.Background(), BackfillRequest{
		Chats:  []ChatSpec{{ID: chatID}},
		Cursor: Cursor{PerChat: map[int64]int{chatID: 42}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calledWith != 42 {
		t.Fatalf("want FetchHistory called with beforeMsgID=42, got %d", calledWith)
	}
}

type callTrackingFetcher struct {
	fn func(ctx context.Context, chatID int64, beforeMsgID int, limit int) ([]RawMessage, bool, error)
}

func (f *callTrackingFetcher) FetchHistory(ctx context.Context, chatID int64, beforeMsgID, limit int) ([]RawMessage, bool, error) {
	return f.fn(ctx, chatID, beforeMsgID, limit)
}

func TestBackfill_IdempotentPersist(t *testing.T) {
	db := testDB(t)
	if db == nil {
		t.Skip("set SCPBOT_TEST_DB to run ent-backed tests")
	}

	chatID := int64(-100123)
	msgs := []RawMessage{
		{ChatID: chatID, MessageID: 1, Date: time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC), Text: "hello", SenderID: 1},
		{ChatID: chatID, MessageID: 2, Date: time.Date(2026, 6, 1, 10, 5, 0, 0, time.UTC), Text: "world", SenderID: 1},
	}

	// First backfill.
	fetcher1 := &fakeFetcher{pages: [][]RawMessage{msgs}}
	b1, err := NewBackfiller(db, BackfillOptions{Fetcher: fetcher1})
	if err != nil {
		t.Fatal(err)
	}
	r1, err := b1.Backfill(context.Background(), BackfillRequest{
		Chats: []ChatSpec{{ID: chatID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r1.TotalMessages != 2 {
		t.Fatalf("first run: want 2 messages, got %d", r1.TotalMessages)
	}

	// Second backfill with same messages (backfill)
	fetcher2 := &fakeFetcher{pages: [][]RawMessage{msgs}}
	b2, err := NewBackfiller(db, BackfillOptions{Fetcher: fetcher2})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := b2.Backfill(context.Background(), BackfillRequest{
		Chats:  []ChatSpec{{ID: chatID}},
		Cursor: Cursor{PerChat: map[int64]int{chatID: 0}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Upsert should not duplicate.
	count, err := db.TelegramMessage.Query().Count(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("want 2 total messages after idempotent backfill, got %d", count)
	}
	_ = r2
}

// testDB opens a test database if SCPBOT_TEST_DB is set.
func testDB(t *testing.T) *ent.Client {
	t.Helper()
	dsn := os.Getenv("SCPBOT_TEST_DB")
	if dsn == "" {
		return nil
	}

	db, err := ent.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
