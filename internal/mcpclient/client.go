// Package mcpclient is an MCP client used to call tools exposed by cmd/ssmcp.
package mcpclient

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.uber.org/zap"

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
//
// The go-sdk session's SSE stream permanently fails after a handful of
// reconnect attempts without progress (mcp.ErrConnectionClosed); every call
// on that session then fails forever. Client reconnects transparently: a
// session-level failure triggers one reconnect-and-retry (the reconnect dial
// itself is retried with backoff) using the same mcpClient/transport that
// produced the original session.
type Client struct {
	mcpClient *mcp.Client
	transport *mcp.StreamableClientTransport

	mu      sync.RWMutex
	session *mcp.ClientSession

	m *mcpMetrics
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
		mcpClient: mcpClient,
		transport: streamableClient,
		session:   session,
		m:         m,
	}, nil
}

// getSession returns the current session.
func (c *Client) getSession() *mcp.ClientSession {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.session
}

// reconnect replaces a dead session with a freshly connected one. Safe to
// call concurrently: only the first caller after a failure actually
// reconnects, others observe the replaced session.
func (c *Client) reconnect(ctx context.Context, stale *mcp.ClientSession) (*mcp.ClientSession, error) {
	if c.mcpClient == nil || c.transport == nil {
		return nil, errors.New("client not configured for reconnect")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != stale {
		// Another caller already reconnected.
		return c.session, nil
	}
	session, err := backoff.Retry(ctx, func() (*mcp.ClientSession, error) {
		return c.mcpClient.Connect(ctx, c.transport, nil)
	},
		backoff.WithBackOff(backoff.NewExponentialBackOff()),
		backoff.WithMaxTries(3),
		backoff.WithNotify(func(err error, d time.Duration) {
			zctx.From(ctx).Warn("retrying mcp reconnect", zap.Error(err), zap.Duration("delay", d))
		}),
	)
	if err != nil {
		return nil, errors.Wrap(err, "reconnect to mcp")
	}
	c.session = session
	return session, nil
}

// withSession runs fn against the current session, reconnecting once and
// retrying if the session has permanently failed.
func withSession[T any](ctx context.Context, c *Client, fn func(*mcp.ClientSession) (T, error)) (T, error) {
	session := c.getSession()
	res, err := fn(session)
	if err == nil || !errors.Is(err, mcp.ErrConnectionClosed) {
		return res, err
	}
	newSession, rerr := c.reconnect(ctx, session)
	if rerr != nil {
		var zero T
		return zero, err
	}
	return fn(newSession)
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
	return c.getSession().Close()
}
