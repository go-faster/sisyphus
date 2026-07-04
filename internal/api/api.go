// Package api implements the generated ogen Handler, bridging HTTP requests to
// the retrieval service and answerer.
package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-faster/errors"
	"github.com/ogen-go/ogen/ogenerrors"

	"github.com/go-faster/sisyphus/internal/index"
	"github.com/go-faster/sisyphus/internal/oas"
)

// Retriever is the retrieval interface Handler needs.
type Retriever interface {
	Retrieve(ctx context.Context, q index.Query) ([]index.Result, error)
}

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
	// Special case for security errors: return 401 instead of 500.
	if _, ok := errors.Into[*ogenerrors.SecurityError](err); ok {
		return &oas.ErrorStatusCode{
			StatusCode: http.StatusUnauthorized,
			Response:   oas.Error{ErrorMessage: "unauthorized"},
		}
	}

	return &oas.ErrorStatusCode{
		StatusCode: http.StatusInternalServerError,
		Response:   oas.Error{ErrorMessage: err.Error()},
	}
}

// ErrorHandler maps ogen security failures to 401; everything else falls
// back to ogen's default handling.
func ErrorHandler(ctx context.Context, w http.ResponseWriter, r *http.Request, err error) {
	var secErr *ogenerrors.SecurityError
	if errors.As(err, &secErr) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(oas.Error{ErrorMessage: "unauthorized"})
		return
	}
	ogenerrors.DefaultErrorHandler(ctx, w, r, err)
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
