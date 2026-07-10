// Package apiclient adapts internal/oas.Client to the Retriever/Answerer
// interfaces used by internal/bot and internal/mcpserver, so those
// binaries talk to the sisyphus API over HTTP instead of holding an
// in-process retrieval/answer stack.
package apiclient

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-faster/errors"
	ht "github.com/ogen-go/ogen/http"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/go-faster/sisyphus/internal/index"
	"github.com/go-faster/sisyphus/internal/oas"
)

// Options configures an apiclient.Client.
type Options struct {
	HTTPClient     ht.Client
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
}

func (o *Options) setDefaults() {
	if o.HTTPClient == nil {
		o.HTTPClient = http.DefaultClient
	}
	if o.TracerProvider == nil {
		o.TracerProvider = otel.GetTracerProvider()
	}
	if o.MeterProvider == nil {
		o.MeterProvider = otel.GetMeterProvider()
	}
}

// Client wraps an oas.Client and implements Retrieve/Answer over HTTP.
type Client struct {
	inv        *oas.Client
	baseURL    string
	httpClient ht.Client
	tracer     trace.Tracer
	m          *clientMetrics
}

// New builds a Client pointed at baseURL, authenticating with a static
// bearer token.
func New(baseURL, token string, opts Options) (*Client, error) {
	opts.setDefaults()
	m, err := newClientMetrics(opts.MeterProvider)
	if err != nil {
		return nil, errors.Wrap(err, "apiclient metrics")
	}
	clientOpts := []oas.ClientOption{
		oas.WithClient(opts.HTTPClient),
		oas.WithTracerProvider(opts.TracerProvider),
		oas.WithMeterProvider(opts.MeterProvider),
	}
	c, err := oas.NewClient(baseURL, staticBearer{token: token}, clientOpts...)
	if err != nil {
		return nil, errors.Wrap(err, "oas client")
	}
	return &Client{
		inv:        c,
		baseURL:    baseURL,
		httpClient: opts.HTTPClient,
		tracer:     opts.TracerProvider.Tracer("github.com/go-faster/sisyphus/apiclient"),
		m:          m,
	}, nil
}

// CheckHealth verifies that the upstream API is ready to serve traffic.
func (c *Client) CheckHealth(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.baseURL, "/")+"/readyz", http.NoBody)
	if err != nil {
		return errors.Wrap(err, "create ready request")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return errors.Wrap(err, "get ready")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("upstream not ready")
	}
	return nil
}

// Answer implements the Answerer interface.
// The results are not sent over the wire; /context performs its own
// server-side retrieval pass, so the caller's results only affect telemetry.
func (c *Client) Answer(ctx context.Context, question string, results []index.Result) (answer string, rerr error) {
	return c.AnswerQuery(ctx, index.Query{Text: question}, results)
}

// AnswerQuery answers using /context with the same query controls as retrieval.
func (c *Client) AnswerQuery(ctx context.Context, q index.Query, results []index.Result) (string, error) {
	ans, err := c.AnswerQueryRich(ctx, q, results)
	if err != nil {
		return "", err
	}
	return ans.Text, nil
}

// AnswerQueryRich answers using /context and returns the structured answer,
// including any source-link buttons the server surfaced.
func (c *Client) AnswerQueryRich(ctx context.Context, q index.Query, results []index.Result) (answer index.Answer, rerr error) {
	start := time.Now()
	ctx, span := c.tracer.Start(ctx, "apiclient.Answer",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.Int("question.length", len(q.Text)),
			attribute.Int("results.count", len(results)),
		),
	)
	defer func() {
		c.m.record(ctx, "answer", time.Since(start).Seconds(), len(results), rerr)
		if rerr != nil {
			span.RecordError(rerr)
			span.SetStatus(codes.Error, rerr.Error())
		}
		span.End()
	}()
	req := &oas.ContextRequest{
		Question:   q.Text,
		Service:    oas.NewOptString(q.Service),
		SourceTier: oas.NewOptString(q.SourceTier),
	}
	if q.Filters != nil {
		req.Filters = oas.NewOptContextRequestFilters(oas.ContextRequestFilters(q.Filters))
	}
	if q.SourcePrefixes != nil {
		req.SourcePrefixes = q.SourcePrefixes
	}
	resp, err := c.inv.Context(ctx, req)
	if err != nil {
		rerr = errors.Wrap(err, "get context")
		return index.Answer{}, rerr
	}
	answer = index.Answer{Text: resp.Answer, Links: fromLinks(resp.Buttons)}
	span.SetAttributes(
		attribute.Int("answer.length", len(answer.Text)),
		attribute.Int("answer.links", len(answer.Links)),
	)
	return answer, nil
}

// fromLinks maps oas links to index links, dropping any that are not valid
// absolute http(s) URLs (defense in depth against a misbehaving server).
func fromLinks(links []oas.Link) []index.Link {
	if len(links) == 0 {
		return nil
	}
	out := make([]index.Link, 0, len(links))
	for _, l := range links {
		il := index.Link{Text: l.Text, URL: l.URL}
		if il.Valid() {
			out = append(out, il)
		}
	}
	return out
}

// Retrieve implements the Retriever interface.
func (c *Client) Retrieve(ctx context.Context, q index.Query) (_ []index.Result, rerr error) {
	start := time.Now()
	ctx, span := c.tracer.Start(ctx, "apiclient.Retrieve",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.Int("query.length", len(q.Text)),
			attribute.Int("query.limit", q.Limit),
		),
	)
	defer func() {
		if rerr != nil {
			span.RecordError(rerr)
			span.SetStatus(codes.Error, rerr.Error())
		}
		span.End()
	}()
	req := &oas.SearchRequest{
		Query:      q.Text,
		Service:    oas.NewOptString(q.Service),
		SourceTier: oas.NewOptString(q.SourceTier),
	}
	if q.Filters != nil {
		req.Filters = oas.NewOptSearchRequestFilters(oas.SearchRequestFilters(q.Filters))
	}
	if q.SourcePrefixes != nil {
		req.SourcePrefixes = q.SourcePrefixes
	}
	if q.Limit > 0 {
		req.Limit = oas.NewOptInt32(int32(q.Limit))
	}

	resp, err := c.inv.Search(ctx, req)
	if err != nil {
		rerr = errors.Wrap(err, "search")
		c.m.record(ctx, "retrieve", time.Since(start).Seconds(), 0, rerr)
		return nil, rerr
	}

	results := make([]index.Result, 0, len(resp.Results))
	for _, sr := range resp.Results {
		chunk := index.Chunk{
			ID:         sr.ChunkID,
			DocumentID: sr.DocumentID,
			Text:       sr.Text,
			Title:      sr.Title.Or(""),
			Type:       index.ChunkType(sr.ChunkType.Or("")),
			Metadata:   make(map[string]any),
		}
		if s := sr.Source.Or(""); s != "" {
			chunk.Metadata["source"] = s
		}
		if u := sr.SourceURL.Or(""); u != "" {
			chunk.Metadata["source_url"] = u
		}

		result := index.Result{
			Chunk:  chunk,
			Score:  sr.Score,
			Vector: sr.Vector.Or(false),
		}
		results = append(results, result)
	}

	c.m.record(ctx, "retrieve", time.Since(start).Seconds(), len(results), nil)
	span.SetAttributes(attribute.Int("results.count", len(results)))
	return results, nil
}

type staticBearer struct{ token string }

func (s staticBearer) BearerAuth(_ context.Context, _ oas.OperationName) (oas.BearerAuth, error) {
	return oas.BearerAuth{Token: s.token}, nil
}
