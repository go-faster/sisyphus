// Package apiclient adapts internal/oas.Client to the Retriever/Answerer
// interfaces used by internal/bot and internal/mcpserver, so those
// binaries talk to the sisyphus API over HTTP instead of holding an
// in-process retrieval/answer stack.
package apiclient

import (
	"context"

	"github.com/go-faster/errors"
	ht "github.com/ogen-go/ogen/http"
	"go.opentelemetry.io/otel"
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
	if o.TracerProvider == nil {
		o.TracerProvider = otel.GetTracerProvider()
	}
	if o.MeterProvider == nil {
		o.MeterProvider = otel.GetMeterProvider()
	}
}

// Client wraps an oas.Client and implements Retrieve/Answer over HTTP.
type Client struct {
	inv *oas.Client
}

// New builds a Client pointed at baseURL, authenticating with a static
// bearer token.
func New(baseURL, token string, opts Options) (*Client, error) {
	opts.setDefaults()
	clientOpts := []oas.ClientOption{
		oas.WithTracerProvider(opts.TracerProvider),
		oas.WithMeterProvider(opts.MeterProvider),
	}
	if opts.HTTPClient != nil {
		clientOpts = append(clientOpts, oas.WithClient(opts.HTTPClient))
	}
	c, err := oas.NewClient(baseURL, staticBearer{token: token}, clientOpts...)
	if err != nil {
		return nil, errors.Wrap(err, "oas client")
	}
	return &Client{inv: c}, nil
}

// Answer implements the Answerer interface.
// The results parameter is intentionally unused because /context performs its
// own independent server-side retrieval pass; this is an accepted phase-1
// trade-off (two retrieval passes instead of one) since ContextRequest has no
// field to carry pre-fetched results — flagged as a future improvement, not
// fixed now.
func (c *Client) Answer(ctx context.Context, question string, _ []index.Result) (string, error) {
	req := &oas.ContextRequest{
		Question: question,
	}
	resp, err := c.inv.Context(ctx, req)
	if err != nil {
		return "", errors.Wrap(err, "context")
	}
	return resp.Answer, nil
}

// Retrieve implements the Retriever interface.
func (c *Client) Retrieve(ctx context.Context, q index.Query) ([]index.Result, error) {
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
		return nil, errors.Wrap(err, "search")
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

	return results, nil
}

type staticBearer struct{ token string }

func (s staticBearer) BearerAuth(_ context.Context, _ oas.OperationName) (oas.BearerAuth, error) {
	return oas.BearerAuth{Token: s.token}, nil
}
