// Package mcpclient is an MCP client used to call tools exposed by cmd/ssmcp.
package mcpclient

import (
	"context"
	"net/http"

	"github.com/go-faster/errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Options configures a new MCP Client.
type Options struct {
	URL     string
	Headers map[string]string
}

// Client is a wrapper around an MCP client session that acts as a ToolSource.
type Client struct {
	session *mcp.ClientSession
	m       *mcpMetrics
}

// New creates and connects a new MCP Client to the gateway.
func New(ctx context.Context, opts Options) (*Client, error) {
	transport := http.DefaultTransport

	if len(opts.Headers) > 0 {
		transport = &headerTransport{
			headers: opts.Headers,
			base:    http.DefaultTransport,
		}
	}

	streamableClient := &mcp.StreamableClientTransport{
		Endpoint: opts.URL,
		HTTPClient: &http.Client{
			Transport: transport,
		},
	}

	mcpClient := mcp.NewClient(
		&mcp.Implementation{Name: "ssagent-mcpclient", Version: "0.1.0"},
		nil,
	)

	session, err := mcpClient.Connect(ctx, streamableClient, nil)
	if err != nil {
		return nil, errors.Wrap(err, "connect to mcp")
	}

	m, _ := newMCPMetrics()

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
