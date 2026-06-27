// Package telegram chunks normalized Telegram support-request Documents into a
// request summary chunk and a raw excerpt chunk (plan §4). It implements
// index.Chunker and is pure: it reads only the Document body and metadata.
package telegram

import (
	"context"
	"maps"

	"github.com/go-faster/scpbot/internal/index"
)

// excerptRunes caps the raw excerpt so we never embed huge noisy chats (plan §4).
const excerptRunes = 2000

// Chunker turns a Telegram conversation Document into chunks.
type Chunker struct{}

var _ index.Chunker = (*Chunker)(nil)

// New builds a Telegram chunker.
func New() *Chunker { return &Chunker{} }

// Chunk produces a telegram_request_summary chunk (from a precomputed summary in
// metadata, falling back to the raw text) and a telegram_raw_excerpt chunk.
func (c *Chunker) Chunk(_ context.Context, doc index.Document) ([]index.Chunk, error) {
	var chunks []index.Chunk
	add := func(t index.ChunkType, title, text string) {
		if text == "" {
			return
		}
		chunks = append(chunks, index.Chunk{
			ID:         index.NewID(),
			DocumentID: doc.ID,
			Index:      len(chunks),
			Type:       t,
			Title:      title,
			Text:       text,
			TextHash:   index.Hash(text),
			Metadata:   cloneMeta(doc.Metadata),
		})
	}

	summary, _ := doc.Metadata["summary"].(string)
	if summary == "" {
		summary = truncate(doc.Body, excerptRunes)
	}
	add(index.ChunkTelegramRequestSummary, doc.Title, summary)
	add(index.ChunkTelegramRawExcerpt, doc.Title, truncate(doc.Body, excerptRunes))
	return chunks, nil
}

func cloneMeta(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	maps.Copy(out, m)
	return out
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + " …"
}
