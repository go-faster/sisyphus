// Package apiclient adapts internal/oas.Client to the Retriever/Answerer
// interfaces used by internal/bot and internal/mcpserver, so those
// binaries talk to the sisyphus API over HTTP instead of holding an
// in-process retrieval/answer stack.
package apiclient

import (
	"context"
	"net/http"
	"time"

	"github.com/go-faster/errors"
	ht "github.com/ogen-go/ogen/http"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
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
		o.MeterProvider = metricnoop.NewMeterProvider()
	}
}

// Client wraps an oas.Client and implements Retrieve/Answer over HTTP.
type Client struct {
	inv    *oas.Client
	tracer trace.Tracer
	m      *clientMetrics
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
		inv:    c,
		tracer: opts.TracerProvider.Tracer("github.com/go-faster/sisyphus/apiclient"),
		m:      m,
	}, nil
}

// Answer implements the Answerer interface.
// The results are not sent over the wire; /context performs its own
// server-side retrieval pass, so the caller's results only affect telemetry.
func (c *Client) Answer(ctx context.Context, question string, results []index.Result) (answer string, rerr error) {
	start := time.Now()
	ctx, span := c.tracer.Start(ctx, "apiclient.Answer",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.Int("question.length", len(question)),
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
		Question: question,
	}
	resp, err := c.inv.Context(ctx, req)
	if err != nil {
		rerr = errors.Wrap(err, "get context")
		return "", rerr
	}
	answer = resp.Answer
	span.SetAttributes(attribute.Int("answer.length", len(answer)))
	return answer, nil
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
		Query:   q.Text,
		Service: oas.NewOptString(q.Service),
	}
	if q.Filters != nil {
		req.Filters = oas.NewOptSearchRequestFilters(oas.SearchRequestFilters(q.Filters))
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
