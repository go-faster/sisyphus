package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/index"
)

type fakeFetcher struct {
	resp index.FetchResponse
	err  error
}

func (f *fakeFetcher) Fetch(ctx context.Context, req index.FetchRequest) (index.FetchResponse, error) {
	return f.resp, f.err
}

func TestFetchHandler_NilFetcher(t *testing.T) {
	handler := fetchHandler(nil)
	ctx := t.Context()

	result, out, err := handler(ctx, nil, FetchArgs{})

	require.Nil(t, err, "Go error should be nil")
	require.NotNil(t, result, "CallToolResult should not be nil")
	require.True(t, result.IsError, "CallToolResult should be error")
	require.Equal(t, "url fetcher not configured", result.Content[0].(*mcp.TextContent).Text)
	require.Equal(t, FetchOut{}, out, "output should be empty")
}

func TestFetchHandler_SuccessfulFetch(t *testing.T) {
	resp := index.FetchResponse{
		StatusCode: 200,
		Body:       "Hello, world!",
		FromSite:   "example.com",
		Truncated:  false,
		Headers: map[string]string{
			"Content-Type": "text/plain",
		},
	}
	fetcher := &fakeFetcher{resp: resp}
	handler := fetchHandler(fetcher)
	ctx := t.Context()

	args := FetchArgs{
		URL:    "https://example.com",
		Method: "GET",
	}

	result, out, err := handler(ctx, nil, args)

	require.Nil(t, err, "Go error should be nil")
	require.Nil(t, result, "CallToolResult should be nil on success")
	require.Equal(t, 200, out.StatusCode)
	require.Equal(t, "Hello, world!", out.Body)
	require.Equal(t, "example.com", out.FromSite)
	require.False(t, out.Truncated)
	require.Equal(t, map[string]string{"Content-Type": "text/plain"}, out.Headers)
}

func TestFetchHandler_FetchError(t *testing.T) {
	fetcher := &fakeFetcher{err: index.ErrURLNotAllowed}
	handler := fetchHandler(fetcher)
	ctx := t.Context()

	args := FetchArgs{
		URL: "https://disallowed.com",
	}

	result, out, err := handler(ctx, nil, args)

	require.Nil(t, err, "Go error should be nil")
	require.NotNil(t, result, "CallToolResult should not be nil")
	require.True(t, result.IsError, "CallToolResult should be error")
	require.Equal(t, "fetch url: url not in allowlist", result.Content[0].(*mcp.TextContent).Text)
	require.Equal(t, FetchOut{}, out, "output should be empty")
}

func TestFetchHandler_PassesThroughArgs(t *testing.T) {
	var capturedArg index.FetchRequest
	fetcher := &capturingFetcher{
		captured: &capturedArg,
		resp:     index.FetchResponse{StatusCode: 204},
	}

	handler := fetchHandler(fetcher)
	ctx := t.Context()

	args := FetchArgs{
		URL:    "https://example.com/path",
		Method: "POST",
		Body:   "test body",
		Headers: map[string]string{
			"X-Custom": "value",
		},
	}

	result, _, err := handler(ctx, nil, args)

	require.Nil(t, err)
	require.Nil(t, result)
	require.Equal(t, "https://example.com/path", capturedArg.URL)
	require.Equal(t, "POST", capturedArg.Method)
	require.Equal(t, "test body", capturedArg.Body)
	require.Equal(t, map[string]string{"X-Custom": "value"}, capturedArg.Headers)
}

type capturingFetcher struct {
	captured *index.FetchRequest
	resp     index.FetchResponse
	err      error
}

func (f *capturingFetcher) Fetch(ctx context.Context, req index.FetchRequest) (index.FetchResponse, error) {
	*f.captured = req
	return f.resp, f.err
}
