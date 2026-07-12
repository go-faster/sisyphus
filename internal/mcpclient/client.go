// Package mcpclient is an MCP client used to call tools exposed by cmd/ssmcp.
package mcpclient

import (
	"context"
	"net/http"

	"github.com/go-faster/errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	"github.com/go-faster/sisyphus/internal/cliversion"
)

// Options configures a new MCP Client.
type Options struct {
	URL           string
	Headers       map[string]string
	HTTPClient    *http.Client
	Version       string
	MeterProvider metric.MeterProvider
}

func (opts *Options) setDefaults() {
	if opts.MeterProvider == nil {
		opts.MeterProvider = otel.GetMeterProvider()
	}
	if opts.Version == "" {
		if info, ok := cliversion.GetInfo("github.com/go-faster/sisyphus"); ok {
			opts.Version = info.Short()
		}
	}
}

// Client is a wrapper around an MCP client session that acts as a ToolSource.
type Client struct {
	session *mcp.ClientSession
	m       *mcpMetrics
}

// New creates and connects a new MCP Client to the gateway.
func New(ctx context.Context, opts Options) (*Client, error) {
	opts.setDefaults()
	transport := http.DefaultTransport

	if len(opts.Headers) > 0 {
		transport = &headerTransport{
			headers: opts.Headers,
			base:    http.DefaultTransport,
		}
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Transport: transport}
	} else if len(opts.Headers) > 0 {
		base := httpClient.Transport
		if base == nil {
			base = http.DefaultTransport
		}
		httpClient = &http.Client{Transport: &headerTransport{headers: opts.Headers, base: base}}
	}

	streamableClient := &mcp.StreamableClientTransport{
		Endpoint:   opts.URL,
		HTTPClient: httpClient,
	}

	mcpClient := mcp.NewClient(
		&mcp.Implementation{Name: "ssagent-mcpclient", Version: opts.Version},
		nil,
	)

	session, err := mcpClient.Connect(ctx, streamableClient, nil)
	if err != nil {
		return nil, errors.Wrap(err, "connect to mcp")
	}

	m, _ := newMCPMetrics(opts.MeterProvider)

	return &Client{
		session: session,
		m:       m,
	}, nil
}

type headerTransport struct {
	headers map[string]string
	base    http.RoundTripper
}

func (h *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req2 := req.Clone(req.Context())
	for k, v := range h.headers {
		req2.Header.Set(k, v)
	}
	return h.base.RoundTrip(req2)
}

// Close closes the underlying MCP session.
func (c *Client) Close() error {
	return c.session.Close()
}
