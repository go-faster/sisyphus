package mcpclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/api"
	"github.com/go-faster/sisyphus/internal/apiclient"
	"github.com/go-faster/sisyphus/internal/index"
	"github.com/go-faster/sisyphus/internal/mcpserver"
	"github.com/go-faster/sisyphus/internal/oas"
)

type e2eBackend struct {
	mu sync.Mutex

	searchQueries []index.Query
	answerCalls   []answerCall
	fileRequests  []index.ContentRequest
	fetchRequests []index.FetchRequest

	result index.Result
}

type answerCall struct {
	query   index.Query
	results int
}

func (b *e2eBackend) Retrieve(_ context.Context, q index.Query) ([]index.Result, error) {
	b.mu.Lock()
	b.searchQueries = append(b.searchQueries, q)
	result := b.result
	b.mu.Unlock()

	return []index.Result{result}, nil
}

func (b *e2eBackend) Answer(_ context.Context, q index.Query, results []index.Result) (index.Answer, error) {
	b.mu.Lock()
	b.answerCalls = append(b.answerCalls, answerCall{query: q, results: len(results)})
	b.mu.Unlock()

	return index.Answer{Text: fmt.Sprintf("answer:%s:%d", q.Text, len(results))}, nil
}

func (b *e2eBackend) ResolveContent(_ context.Context, req index.ContentRequest) (index.ContentResponse, error) {
	b.mu.Lock()
	b.fileRequests = append(b.fileRequests, req)
	b.mu.Unlock()

	return index.ContentResponse{
		Content: fmt.Sprintf("file:%s|%s|%s|%d|%d", req.Repo, req.Path, req.Branch, req.Start, req.End),
		Source:  "database",
		Found:   true,
	}, nil
}

func (b *e2eBackend) Fetch(_ context.Context, req index.FetchRequest) (index.FetchResponse, error) {
	b.mu.Lock()
	b.fetchRequests = append(b.fetchRequests, req)
	b.mu.Unlock()

	return index.FetchResponse{
		StatusCode: http.StatusAccepted,
		Body:       fmt.Sprintf("fetch:%s|%s|%s|%s", req.URL, req.Method, req.Body, req.Headers["X-Trace-ID"]),
		FromSite:   "example.com",
		Headers:    map[string]string{"Content-Type": "text/plain"},
	}, nil
}

func TestClientPublicToolsEndToEnd(t *testing.T) {
	ctx := t.Context()

	backend := &e2eBackend{
		result: index.Result{
			Chunk: index.Chunk{
				ID:         uuid.MustParse("00000000-0000-0000-0000-000000000001"),
				DocumentID: uuid.MustParse("00000000-0000-0000-0000-000000000002"),
				Title:      "Billing FAQ",
				Text:       "How to change plan?",
				Type:       index.ChunkSection,
				Metadata: map[string]any{
					"source":     "git_docs:billing",
					"source_url": "https://example.com/docs/billing",
				},
			},
			Score:  0.97,
			Vector: true,
		},
	}

	apiToken := "api-token"
	apiHandler := api.New(backend, backend, "test",
		api.WithContentResolver(backend),
		api.WithURLFetcher(backend),
	)
	apiServer, err := oas.NewServer(apiHandler, api.NewSecurityHandler(apiToken), oas.WithErrorHandler(api.ErrorHandler))
	require.NoError(t, err)
	apiHTTP := httptest.NewServer(apiServer)
	t.Cleanup(apiHTTP.Close)

	apiClient, err := apiclient.New(apiHTTP.URL, apiToken, apiclient.Options{})
	require.NoError(t, err)

	mcpToken := "mcp-token"
	srv := mcpserver.New(apiClient, apiClient, apiClient, apiClient, "test")
	var mcpHandler http.Handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	mcpHandler = mcpserver.BearerAuthMiddleware(mcpToken)(mcpHandler)
	mcpMux := http.NewServeMux()
	mcpMux.Handle("/mcp", mcpHandler)
	mcpHTTP := httptest.NewServer(mcpMux)
	t.Cleanup(mcpHTTP.Close)

	client, err := New(ctx, Options{
		URL: mcpHTTP.URL + "/mcp",
		Headers: map[string]string{
			"Authorization": "Bearer " + mcpToken,
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	tools, err := client.Tools(ctx)
	require.NoError(t, err)
	require.Len(t, tools, 4)

	toolNames := make([]string, 0, len(tools))
	for _, tool := range tools {
		require.NotNil(t, tool.OfFunction)
		toolNames = append(toolNames, tool.OfFunction.Function.Name)
	}
	require.ElementsMatch(t, []string{
		"search_knowledge",
		"answer_question",
		"get_file_content",
		"fetch_url",
	}, toolNames)

	searchOut, err := client.Call(ctx, "search_knowledge", []byte(`{"query":"change plan","service":"billing","filters":{"repo":"docs","status":"open"},"source_tier":"code","source_prefixes":["git_docs:"],"limit":7}`))
	require.NoError(t, err)
	require.Contains(t, searchOut, "Billing FAQ")
	require.Contains(t, searchOut, "https://example.com/docs/billing")

	answerOut, err := client.Call(ctx, "answer_question", []byte(`{"question":"How to change plan?","service":"billing","filters":{"repo":"docs","status":"open"},"source_tier":"code","source_prefixes":["git_docs:"]}`))
	require.NoError(t, err)
	require.Contains(t, answerOut, "answer:How to change plan?:1")
	require.Contains(t, answerOut, "Billing FAQ")

	fileOut, err := client.Call(ctx, "get_file_content", []byte(`{"repo":"docs-repo","path":"guide.md","branch":"main","start":3,"end":9}`))
	require.NoError(t, err)
	require.Contains(t, fileOut, "file:docs-repo|guide.md|main|3|9")

	fetchOut, err := client.Call(ctx, "fetch_url", []byte(`{"url":"https://example.com/docs/billing","method":"POST","body":"payload=1","headers":{"X-Trace-ID":"trace-123"}}`))
	require.NoError(t, err)
	require.Contains(t, fetchOut, "fetch:https://example.com/docs/billing|POST|payload=1|trace-123")

	require.Len(t, backend.searchQueries, 3)
	require.Equal(t, index.Query{
		Text:           "change plan",
		Service:        "billing",
		Filters:        map[string]string{"repo": "docs", "status": "open"},
		SourceTier:     "code",
		SourcePrefixes: []string{"git_docs:"},
		Limit:          7,
	}, backend.searchQueries[0])
	require.Equal(t, index.Query{
		Text:           "How to change plan?",
		Service:        "billing",
		Filters:        map[string]string{"repo": "docs", "status": "open"},
		SourceTier:     "code",
		SourcePrefixes: []string{"git_docs:"},
		Limit:          12,
	}, backend.searchQueries[1])
	require.Equal(t, backend.searchQueries[1], backend.searchQueries[2])

	require.Len(t, backend.answerCalls, 1)
	require.Equal(t, answerCall{query: index.Query{
		Text:           "How to change plan?",
		Service:        "billing",
		Filters:        map[string]string{"repo": "docs", "status": "open"},
		SourceTier:     "code",
		SourcePrefixes: []string{"git_docs:"},
		Limit:          12,
	}, results: 1}, backend.answerCalls[0])

	require.Len(t, backend.fileRequests, 1)
	require.Equal(t, index.ContentRequest{Repo: "docs-repo", Path: "guide.md", Branch: "main", Start: 3, End: 9}, backend.fileRequests[0])

	require.Len(t, backend.fetchRequests, 1)
	require.Equal(t, index.FetchRequest{
		URL:    "https://example.com/docs/billing",
		Method: "POST",
		Body:   "payload=1",
		Headers: map[string]string{
			"X-Trace-ID": "trace-123",
		},
	}, backend.fetchRequests[0])
}

func TestClientRejectsInvalidMCPToken(t *testing.T) {
	ctx := t.Context()

	apiToken := "api-token"
	backend := &e2eBackend{result: index.Result{}}
	apiHandler := api.New(backend, backend, "test")
	apiServer, err := oas.NewServer(apiHandler, api.NewSecurityHandler(apiToken), oas.WithErrorHandler(api.ErrorHandler))
	require.NoError(t, err)
	apiHTTP := httptest.NewServer(apiServer)
	t.Cleanup(apiHTTP.Close)

	apiClient, err := apiclient.New(apiHTTP.URL, apiToken, apiclient.Options{})
	require.NoError(t, err)

	mcpToken := "mcp-token"
	srv := mcpserver.New(apiClient, apiClient, apiClient, apiClient, "test")
	var mcpHandler http.Handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	mcpHandler = mcpserver.BearerAuthMiddleware(mcpToken)(mcpHandler)
	mcpMux := http.NewServeMux()
	mcpMux.Handle("/mcp", mcpHandler)
	mcpHTTP := httptest.NewServer(mcpMux)
	t.Cleanup(mcpHTTP.Close)

	_, err = New(ctx, Options{
		URL: mcpHTTP.URL + "/mcp",
		Headers: map[string]string{
			"Authorization": "Bearer wrong-token",
		},
	})
	require.Error(t, err)
}
