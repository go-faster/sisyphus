package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/go-faster/sisyphus/internal/index"
)

type FetchArgs struct {
	URL     string            `json:"url" jsonschema:"The URL to fetch. Must match an operator-approved site."`
	Method  string            `json:"method,omitempty" jsonschema:"HTTP method (default GET). Must be allowed by the site."`
	Body    string            `json:"body,omitempty" jsonschema:"Request body for POST/PUT/PATCH."`
	Headers map[string]string `json:"headers,omitempty" jsonschema:"Additional request headers."`
}

type FetchOut struct {
	StatusCode int               `json:"status_code"`
	Body       string            `json:"body"`
	FromSite   string            `json:"from_site"`
	Truncated  bool              `json:"truncated"`
	Headers    map[string]string `json:"headers,omitempty"`
}

func fetchHandler(f index.URLFetcher) func(context.Context, *mcp.CallToolRequest, FetchArgs) (*mcp.CallToolResult, FetchOut, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args FetchArgs) (*mcp.CallToolResult, FetchOut, error) {
		if f == nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "url fetcher not configured"}},
				IsError: true,
			}, FetchOut{}, nil
		}

		resp, err := f.Fetch(ctx, index.FetchRequest{
			URL:     args.URL,
			Method:  args.Method,
			Body:    args.Body,
			Headers: args.Headers,
		})
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "fetch url: " + err.Error()}},
				IsError: true,
			}, FetchOut{}, nil
		}

		return nil, FetchOut{
			StatusCode: resp.StatusCode,
			Body:       resp.Body,
			FromSite:   resp.FromSite,
			Truncated:  resp.Truncated,
			Headers:    resp.Headers,
		}, nil
	}
}
