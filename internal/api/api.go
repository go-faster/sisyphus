// Package api implements the generated ogen Handler, bridging HTTP requests to
// the retrieval service and answerer.
package api

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"github.com/ogen-go/ogen/ogenerrors"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/index"
	"github.com/go-faster/sisyphus/internal/oas"
)

// Retriever is the retrieval interface Handler needs.
type Retriever interface {
	Retrieve(ctx context.Context, q index.Query) ([]index.Result, error)
}

// AnswerIndexer persists answered questions back into the shared index.
type AnswerIndexer interface {
	Index(ctx context.Context, doc index.Document) error
}

// Option customizes Handler.
type Option func(*Handler)

// Handler implements oas.Handler.
type Handler struct {
	retriever Retriever
	answerer  index.Answerer
	answers   AnswerIndexer
	content   index.ContentResolver
	fetcher   index.URLFetcher
	version   string
}

var _ oas.Handler = (*Handler)(nil)

// New builds an API handler.
func New(r Retriever, a index.Answerer, version string, opts ...Option) *Handler {
	h := &Handler{retriever: r, answerer: a, version: version}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// WithAnswerIndexer enables saving answered questions as indexed documents.
func WithAnswerIndexer(p AnswerIndexer) Option {
	return func(h *Handler) {
		h.answers = p
	}
}

// WithContentResolver sets the file content resolver.
func WithContentResolver(c index.ContentResolver) Option {
	return func(h *Handler) {
		h.content = c
	}
}

// WithURLFetcher sets the whitelisted URL fetcher.
func WithURLFetcher(f index.URLFetcher) Option {
	return func(h *Handler) {
		h.fetcher = f
	}
}

// GetHealth implements the liveness probe.
func (h *Handler) GetHealth(_ context.Context) (*oas.Health, error) {
	return &oas.Health{
		Status:  "ok",
		Version: oas.NewOptString(h.version),
	}, nil
}

// GetFile retrieves actual file content.
func (h *Handler) GetFile(ctx context.Context, req *oas.FileRequest) (*oas.FileResponse, error) {
	if h.content == nil {
		return &oas.FileResponse{Found: false}, nil
	}

	cr := index.ContentRequest{
		Repo:   req.Repo,
		Path:   req.Path,
		Branch: req.Branch.Or(""),
		Start:  int(req.Start.Or(0)),
		End:    int(req.End.Or(0)),
	}

	resp, err := h.content.ResolveContent(ctx, cr)
	if err != nil {
		zctx.From(ctx).Error("failed to resolve file content", zap.Error(err))
		return &oas.FileResponse{Found: false}, nil
	}

	return &oas.FileResponse{
		Content: resp.Content,
		Source:  oas.NewOptString(resp.Source),
		Found:   resp.Found,
	}, nil
}

// FetchURL fetches a URL from the configured allowlist.
func (h *Handler) FetchURL(ctx context.Context, req *oas.FetchURLRequest) (*oas.FetchURLResponse, error) {
	if h.fetcher == nil {
		return nil, &oas.ErrorStatusCode{
			StatusCode: http.StatusForbidden,
			Response:   oas.Error{ErrorMessage: "url fetcher not configured"},
		}
	}

	resp, err := h.fetcher.Fetch(ctx, index.FetchRequest{
		URL:     req.URL,
		Method:  req.Method.Or(""),
		Body:    req.Body.Or(""),
		Headers: req.Headers.Or(nil),
	})
	if err != nil {
		if stderrors.Is(err, index.ErrURLNotAllowed) || stderrors.Is(err, index.ErrFetchMethodNotAllowed) {
			return nil, &oas.ErrorStatusCode{
				StatusCode: http.StatusForbidden,
				Response:   oas.Error{ErrorMessage: err.Error()},
			}
		}
		return nil, errors.Wrap(err, "fetch url")
	}

	out := &oas.FetchURLResponse{
		StatusCode: resp.StatusCode,
		Body:       resp.Body,
		FromSite:   resp.FromSite,
		Truncated:  oas.NewOptBool(resp.Truncated),
	}
	if len(resp.Headers) > 0 {
		out.Headers = oas.NewOptFetchURLResponseHeaders(oas.FetchURLResponseHeaders(resp.Headers))
	}
	return out, nil
}

// Search runs hybrid retrieval.
func (h *Handler) Search(ctx context.Context, req *oas.SearchRequest) (*oas.SearchResponse, error) {
	q := index.Query{
		Text:           req.Query,
		Service:        req.Service.Or(""),
		Filters:        req.Filters.Or(nil),
		SourceTier:     req.SourceTier.Or(""),
		SourcePrefixes: sourcePrefixes(req.Filters.Or(nil), req.SourceTier.Or(""), req.SourcePrefixes),
		Limit:          int(req.Limit.Or(30)),
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
		Text:           req.Question,
		Service:        req.Service.Or(""),
		Filters:        req.Filters.Or(nil),
		SourceTier:     req.SourceTier.Or(""),
		SourcePrefixes: sourcePrefixes(req.Filters.Or(nil), req.SourceTier.Or(""), req.SourcePrefixes),
		Limit:          12,
	}
	results, err := h.retriever.Retrieve(ctx, q)
	if err != nil {
		return nil, errors.Wrap(err, "retrieve")
	}

	// Call the unified Answerer which always returns a full index.Answer
	// including any source-link buttons.
	answer, err := h.answerer.Answer(ctx, q, results)
	if err != nil {
		return nil, errors.Wrap(err, "answer")
	}

	if h.answers != nil {
		if err := h.answers.Index(ctx, answeredQuestionDocument(req.Question, answer.Text)); err != nil {
			zctx.From(ctx).Warn("index answered question failed", zap.Error(err))
		}
	}
	return &oas.ContextResponse{
		Answer:     answer.Text,
		Confidence: oas.NewOptString("low"),
		Buttons:    toLinks(answer.Links),
		Results:    toSearchResults(results),
		Debug:      toDebug(answer.Debug),
	}, nil
}

// toLinks maps index links to their oas representation.
func toLinks(links []index.Link) []oas.Link {
	if len(links) == 0 {
		return nil
	}
	out := make([]oas.Link, 0, len(links))
	for _, l := range links {
		out = append(out, oas.Link{Text: l.Text, URL: l.URL})
	}
	return out
}

// toDebug maps index.Debug to its oas representation; absent from the
// response unless the operator has opted into debug info.
func toDebug(d *index.Debug) oas.OptDebug {
	if d == nil {
		return oas.OptDebug{}
	}
	return oas.NewOptDebug(oas.Debug{
		TraceID:          oas.NewOptString(d.TraceID),
		DurationMs:       oas.NewOptInt64(d.DurationMS),
		Iterations:       oas.NewOptInt(d.Iterations),
		ToolCalls:        oas.NewOptInt(d.ToolCalls),
		PromptTokens:     oas.NewOptInt64(d.PromptTokens),
		CompletionTokens: oas.NewOptInt64(d.CompletionTokens),
	})
}

func answeredQuestionDocument(question, answer string) index.Document {
	now := time.Now()
	question = strings.TrimSpace(question)
	body := "# " + question + "\n\n## Answer\n\n" + strings.TrimSpace(answer) + "\n"
	title := question
	if runes := []rune(title); len(runes) > 120 {
		title = string(runes[:120])
	}
	return index.Document{
		ID:       index.NewID(),
		Source:   index.SourceAnswer,
		SourceID: index.Hash(question),
		Title:    title,
		Body:     body,
		BodyHash: index.Hash(body),
		Metadata: map[string]any{
			"source":    string(index.SourceAnswer),
			"authority": string(index.AuthorityLow),
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// NewError maps a handler error to the default error response.
func (h *Handler) NewError(ctx context.Context, err error) *oas.ErrorStatusCode {
	// Special case for security errors: return 401 instead of 500.
	if _, ok := errors.Into[*ogenerrors.SecurityError](err); ok {
		return &oas.ErrorStatusCode{
			StatusCode: http.StatusUnauthorized,
			Response:   oas.Error{ErrorMessage: "unauthorized"},
		}
	}

	// Log the real error server-side to avoid leaking internal details.
	zctx.From(ctx).Error("api request failed", zap.Error(err))

	return &oas.ErrorStatusCode{
		StatusCode: http.StatusInternalServerError,
		Response:   oas.Error{ErrorMessage: "internal server error"},
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
