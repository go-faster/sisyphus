package telegram

import (
	"context"
	"strings"
	"testing"

	"github.com/go-faster/scpbot/internal/index"
)

func TestChunk(t *testing.T) {
	doc := index.Document{
		ID:     index.NewID(),
		Source: index.SourceTelegram,
		Title:  "Telegram support request (chat 1)",
		Body:   "alice: invoice stuck\nbob: callback received",
		Metadata: map[string]any{
			"source":  string(index.SourceTelegram),
			"summary": "User reports invoice stuck after callback.",
			"service": "billing-api",
		},
	}

	chunks, err := New().Chunk(context.Background(), doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("want 2 chunks, got %d", len(chunks))
	}
	if chunks[0].Type != index.ChunkTelegramRequestSummary {
		t.Fatalf("chunk0 type=%s", chunks[0].Type)
	}
	if chunks[0].Text != "User reports invoice stuck after callback." {
		t.Fatalf("summary chunk used wrong text: %q", chunks[0].Text)
	}
	if chunks[1].Type != index.ChunkTelegramRawExcerpt {
		t.Fatalf("chunk1 type=%s", chunks[1].Type)
	}
	for i, c := range chunks {
		if c.DocumentID != doc.ID {
			t.Fatalf("chunk %d wrong DocumentID", i)
		}
		if c.TextHash == "" || c.Index != i {
			t.Fatalf("chunk %d bad hash/index: %+v", i, c)
		}
		if c.Metadata["service"] != "billing-api" {
			t.Fatalf("chunk %d lost metadata", i)
		}
	}

	// Metadata must be cloned, not shared.
	chunks[0].Metadata["service"] = "mutated"
	if doc.Metadata["service"] != "billing-api" {
		t.Fatal("chunk metadata aliases document metadata")
	}
}

func TestChunkFallsBackToBody(t *testing.T) {
	doc := index.Document{
		ID:       index.NewID(),
		Body:     strings.Repeat("x", 10),
		Metadata: map[string]any{},
	}
	chunks, err := New().Chunk(context.Background(), doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 || chunks[0].Text != doc.Body {
		t.Fatalf("expected summary to fall back to body, got %+v", chunks)
	}
}
