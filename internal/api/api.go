// Package api implements the generated ogen Handler, bridging HTTP requests to
// the retrieval service and answerer.
package api

import (
	"context"

	"github.com/go-faster/errors"

	"github.com/go-faster/scpbot/internal/index"
	"github.com/go-faster/scpbot/internal/oas"
	"github.com/go-faster/scpbot/internal/wire"
)

// Retriever is the retrieval interface (alias to wire.Retriever).
type Retriever = wire.Retriever

// Handler implements oas.Handler.
type Handler struct {
	retriever Retriever
	answerer  index.Answerer
	version   string
}

var _ oas.Handler = (*Handler)(nil)

// New builds an API handler.
func New(r Retriever, a index.Answerer, version string) *Handler {
	return &Handler{retriever: r, answerer: a, version: version}
}

// GetHealth implements the liveness probe.
func (h *Handler) GetHealth(_ context.Context) (*oas.Health, error) {
	return &oas.Health{
		Status:  "ok",
		Version: oas.NewOptString(h.version),
	}, nil
}

// Search runs hybrid retrieval.
func (h *Handler) Search(ctx context.Context, req *oas.SearchRequest) (*oas.SearchResponse, error) {
	q := index.Query{
		Text:    req.Query,
		Service: req.Service.Or(""),
		Filters: req.Filters.Or(nil),
		Limit:   int(req.Limit.Or(30)),
	}
	results, err := h.retriever.Retrieve(ctx, q)
	if err != nil {
		return nil, errors.Wrap(err, "retrieve")
	}
	return &oas.SearchResponse{Results: toSearchResults(results)}, nil
}

// Context answers a question from retrieved context (plan §14).
func (h *Handler) Context(ctx context.Context, req *oas.ContextRequest) (*oas.ContextResponse, error) {
	q := index.Query{
		Text:    req.Question,
		Service: req.Service.Or(""),
		Filters: req.Filters.Or(nil),
		Limit:   12,
	}
	results, err := h.retriever.Retrieve(ctx, q)
	if err != nil {
		return nil, errors.Wrap(err, "retrieve")
	}
	answer, err := h.answerer.Answer(ctx, req.Question, results)
	if err != nil {
		return nil, errors.Wrap(err, "answer")
	}
	return &oas.ContextResponse{
		Answer:     answer,
		Confidence: oas.NewOptString("low"),
		Results:    toSearchResults(results),
	}, nil
}

// NewError maps a handler error to the default error response.
func (h *Handler) NewError(_ context.Context, err error) *oas.ErrorStatusCode {
	return &oas.ErrorStatusCode{
		StatusCode: 500,
		Response:   oas.Error{ErrorMessage: err.Error()},
	}
}

func toSearchResults(rs []index.Result) []oas.SearchResult {
	out := make([]oas.SearchResult, 0, len(rs))
	for _, r := range rs {
		sr := oas.SearchResult{
			ChunkID:    r.Chunk.ID,
			DocumentID: r.Chunk.DocumentID,
			Text:       r.Chunk.Text,
			Score:      r.Score,
			Vector:     oas.NewOptBool(r.Vector),
		}
		if r.Chunk.Title != "" {
			sr.Title = oas.NewOptString(r.Chunk.Title)
		}
		if r.Chunk.Type != "" {
			sr.ChunkType = oas.NewOptString(string(r.Chunk.Type))
		}
		if s := metaString(r.Chunk.Metadata, "source"); s != "" {
			sr.Source = oas.NewOptString(s)
		}
		if u := metaString(r.Chunk.Metadata, "source_url"); u != "" {
			sr.SourceURL = oas.NewOptString(u)
		}
		out = append(out, sr)
	}
	return out
}

func metaString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
