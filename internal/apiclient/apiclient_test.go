package apiclient

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/api"
	"github.com/go-faster/sisyphus/internal/index"
	"github.com/go-faster/sisyphus/internal/oas"
)

// fakeRetriever returns known results for testing.
type fakeRetriever struct{}

func (f *fakeRetriever) Retrieve(ctx context.Context, q index.Query) ([]index.Result, error) {
	id1 := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	id2 := uuid.MustParse("00000000-0000-0000-0000-000000000002")

	return []index.Result{
		{
			Chunk: index.Chunk{
				ID:         id1,
				DocumentID: id2,
				Text:       "test chunk 1",
				Title:      "Test Title",
				Type:       index.ChunkSection,
				Metadata: map[string]any{
					"source":     "git_docs:repo",
					"source_url": "https://example.com/docs",
				},
			},
			Score:  0.95,
			Vector: true,
		},
		{
			Chunk: index.Chunk{
				ID:         uuid.MustParse("00000000-0000-0000-0000-000000000003"),
				DocumentID: id2,
				Text:       "test chunk 2",
				Title:      "",
				Type:       index.ChunkGitCommit,
				Metadata: map[string]any{
					"source": "git_commits:repo",
				},
			},
			Score:  0.75,
			Vector: false,
		},
	}, nil
}

// fakeAnswerer returns a fixed answer.
type fakeAnswerer struct{}

func (f *fakeAnswerer) Answer(ctx context.Context, question string, results []index.Result) (string, error) {
	return "This is the answer to: " + question, nil
}

func TestClientRetrieve(t *testing.T) {
	// Create server with fake handler.
	handler := api.New(&fakeRetriever{}, &fakeAnswerer{}, "v1.0.0")
	secHandler := api.NewSecurityHandler("secret-token")
	server, err := oas.NewServer(handler, secHandler, oas.WithErrorHandler(api.ErrorHandler))
	require.NoError(t, err)

	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	// Create client and verify it can retrieve.
	client, err := New(httpServer.URL, "secret-token", Options{})
	require.NoError(t, err)

	results, err := client.Retrieve(context.Background(), index.Query{
		Text:  "test",
		Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, results, 2)

	// Verify first result
	assert.Equal(t, "test chunk 1", results[0].Chunk.Text)
	assert.Equal(t, "Test Title", results[0].Chunk.Title)
	assert.Equal(t, index.ChunkSection, results[0].Chunk.Type)
	assert.Equal(t, 0.95, results[0].Score)
	assert.True(t, results[0].Vector)
	assert.Equal(t, "git_docs:repo", results[0].Chunk.Metadata["source"])
	assert.Equal(t, "https://example.com/docs", results[0].Chunk.Metadata["source_url"])

	// Verify second result
	assert.Equal(t, "test chunk 2", results[1].Chunk.Text)
	assert.Equal(t, "", results[1].Chunk.Title)
	assert.Equal(t, index.ChunkGitCommit, results[1].Chunk.Type)
	assert.Equal(t, 0.75, results[1].Score)
	assert.False(t, results[1].Vector)
	assert.Equal(t, "git_commits:repo", results[1].Chunk.Metadata["source"])
	// source_url should not be set
	assert.NotContains(t, results[1].Chunk.Metadata, "source_url")
}

func TestClientAnswer(t *testing.T) {
	// Create server with fake handler.
	handler := api.New(&fakeRetriever{}, &fakeAnswerer{}, "v1.0.0")
	secHandler := api.NewSecurityHandler("secret-token")
	server, err := oas.NewServer(handler, secHandler, oas.WithErrorHandler(api.ErrorHandler))
	require.NoError(t, err)

	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	// Create client and verify it can get an answer.
	client, err := New(httpServer.URL, "secret-token", Options{})
	require.NoError(t, err)

	answer, err := client.Answer(context.Background(), "What is the answer?", nil)
	require.NoError(t, err)
	assert.Equal(t, "This is the answer to: What is the answer?", answer)
}

func TestClientWrongToken(t *testing.T) {
	// Create server with fake handler.
	handler := api.New(&fakeRetriever{}, &fakeAnswerer{}, "v1.0.0")
	secHandler := api.NewSecurityHandler("secret-token")
	server, err := oas.NewServer(handler, secHandler, oas.WithErrorHandler(api.ErrorHandler))
	require.NoError(t, err)

	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	// Create client with wrong token.
	client, err := New(httpServer.URL, "wrong-token", Options{})
	require.NoError(t, err) // Client creation should succeed

	// But calls should fail due to auth.
	_, err = client.Retrieve(context.Background(), index.Query{Text: "test"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401") // Unauthorized

	_, err = client.Answer(context.Background(), "test?", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
}

func TestClientRetrieveWithFilters(t *testing.T) {
	// Custom retriever that captures the query.
	captureRetriever := &captureQueryRetriever{}

	handler := api.New(captureRetriever, &fakeAnswerer{}, "v1.0.0")
	secHandler := api.NewSecurityHandler("secret-token")
	server, err := oas.NewServer(handler, secHandler, oas.WithErrorHandler(api.ErrorHandler))
	require.NoError(t, err)

	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	client, err := New(httpServer.URL, "secret-token", Options{})
	require.NoError(t, err)

	query := index.Query{
		Text:       "test",
		SourceTier: "code",
		Filters: map[string]string{
			"status": "open",
			"repo":   "myrepo",
		},
		SourcePrefixes: []string{index.SourceGitCodePrefix},
		Limit:          20,
	}
	_, _ = client.Retrieve(context.Background(), query)

	// Verify the query was passed correctly.
	assert.Equal(t, query.Text, captureRetriever.lastQuery.Text)
	assert.Equal(t, query.Filters, captureRetriever.lastQuery.Filters)
	assert.Equal(t, query.SourceTier, captureRetriever.lastQuery.SourceTier)
	assert.Equal(t, query.SourcePrefixes, captureRetriever.lastQuery.SourcePrefixes)
	assert.Equal(t, query.Limit, captureRetriever.lastQuery.Limit)
}

func TestClientAnswerQuerySourceControls(t *testing.T) {
	captureRetriever := &captureQueryRetriever{}

	handler := api.New(captureRetriever, &fakeAnswerer{}, "v1.0.0")
	secHandler := api.NewSecurityHandler("secret-token")
	server, err := oas.NewServer(handler, secHandler, oas.WithErrorHandler(api.ErrorHandler))
	require.NoError(t, err)

	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	client, err := New(httpServer.URL, "secret-token", Options{})
	require.NoError(t, err)

	query := index.Query{
		Text:           "test?",
		SourceTier:     "history",
		SourcePrefixes: []string{index.SourceGitCommitsPrefix},
	}
	_, err = client.AnswerQuery(context.Background(), query, nil)
	require.NoError(t, err)

	assert.Equal(t, query.Text, captureRetriever.lastQuery.Text)
	assert.Equal(t, query.SourceTier, captureRetriever.lastQuery.SourceTier)
	assert.Equal(t, query.SourcePrefixes, captureRetriever.lastQuery.SourcePrefixes)
}

type captureQueryRetriever struct {
	lastQuery index.Query
}

func (c *captureQueryRetriever) Retrieve(ctx context.Context, q index.Query) ([]index.Result, error) {
	c.lastQuery = q
	return nil, nil
}

// fakeContentResolver for testing GetFile endpoint
type fakeContentResolverAPI struct{}

func (f *fakeContentResolverAPI) ResolveContent(ctx context.Context, req index.ContentRequest) (index.ContentResponse, error) {
	if req.Path == "notfound.txt" {
		return index.ContentResponse{Found: false}, nil
	}
	return index.ContentResponse{
		Content: "file content here",
		Source:  "database",
		Found:   true,
	}, nil
}

// fakeURLFetcherAPI for testing FetchURL endpoint
type fakeURLFetcherAPI struct{}

func (f *fakeURLFetcherAPI) Fetch(ctx context.Context, req index.FetchRequest) (index.FetchResponse, error) {
	if req.URL == "https://forbidden.com/data" {
		return index.FetchResponse{}, index.ErrURLNotAllowed
	}
	if req.Method == "DELETE" {
		return index.FetchResponse{}, index.ErrFetchMethodNotAllowed
	}
	return index.FetchResponse{
		StatusCode: 200,
		Body:       `{"status":"ok"}`,
		FromSite:   "example.com",
		Truncated:  false,
		Headers:    map[string]string{"Content-Type": "application/json"},
	}, nil
}

func TestClientResolveContent_Success(t *testing.T) {
	handler := api.New(&fakeRetriever{}, &fakeAnswerer{}, "v1.0.0",
		api.WithContentResolver(&fakeContentResolverAPI{}))
	secHandler := api.NewSecurityHandler("secret-token")
	server, err := oas.NewServer(handler, secHandler, oas.WithErrorHandler(api.ErrorHandler))
	require.NoError(t, err)

	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	client, err := New(httpServer.URL, "secret-token", Options{})
	require.NoError(t, err)

	resp, err := client.ResolveContent(context.Background(), index.ContentRequest{
		Repo:   "myrepo",
		Path:   "README.md",
		Branch: "main",
	})
	require.NoError(t, err)
	assert.True(t, resp.Found)
	assert.Equal(t, "file content here", resp.Content)
	assert.Equal(t, "database", resp.Source)
}

func TestClientResolveContent_NotFound(t *testing.T) {
	handler := api.New(&fakeRetriever{}, &fakeAnswerer{}, "v1.0.0",
		api.WithContentResolver(&fakeContentResolverAPI{}))
	secHandler := api.NewSecurityHandler("secret-token")
	server, err := oas.NewServer(handler, secHandler, oas.WithErrorHandler(api.ErrorHandler))
	require.NoError(t, err)

	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	client, err := New(httpServer.URL, "secret-token", Options{})
	require.NoError(t, err)

	resp, err := client.ResolveContent(context.Background(), index.ContentRequest{
		Repo: "myrepo",
		Path: "notfound.txt",
	})
	require.NoError(t, err)
	assert.False(t, resp.Found)
	assert.Empty(t, resp.Content)
}

func TestClientFetch_Success(t *testing.T) {
	handler := api.New(&fakeRetriever{}, &fakeAnswerer{}, "v1.0.0",
		api.WithURLFetcher(&fakeURLFetcherAPI{}))
	secHandler := api.NewSecurityHandler("secret-token")
	server, err := oas.NewServer(handler, secHandler, oas.WithErrorHandler(api.ErrorHandler))
	require.NoError(t, err)

	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	client, err := New(httpServer.URL, "secret-token", Options{})
	require.NoError(t, err)

	resp, err := client.Fetch(context.Background(), index.FetchRequest{
		URL:    "https://example.com/api",
		Method: "GET",
	})
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, `{"status":"ok"}`, resp.Body)
	assert.Equal(t, "example.com", resp.FromSite)
	assert.False(t, resp.Truncated)
	assert.Equal(t, "application/json", resp.Headers["Content-Type"])
}

func TestClientFetch_URLNotAllowed(t *testing.T) {
	handler := api.New(&fakeRetriever{}, &fakeAnswerer{}, "v1.0.0",
		api.WithURLFetcher(&fakeURLFetcherAPI{}))
	secHandler := api.NewSecurityHandler("secret-token")
	server, err := oas.NewServer(handler, secHandler, oas.WithErrorHandler(api.ErrorHandler))
	require.NoError(t, err)

	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	client, err := New(httpServer.URL, "secret-token", Options{})
	require.NoError(t, err)

	resp, err := client.Fetch(context.Background(), index.FetchRequest{
		URL: "https://forbidden.com/data",
	})
	require.Error(t, err)
	assert.Empty(t, resp)
	assert.Contains(t, err.Error(), "403") // StatusForbidden
}

func TestClientFetch_MethodNotAllowed(t *testing.T) {
	handler := api.New(&fakeRetriever{}, &fakeAnswerer{}, "v1.0.0",
		api.WithURLFetcher(&fakeURLFetcherAPI{}))
	secHandler := api.NewSecurityHandler("secret-token")
	server, err := oas.NewServer(handler, secHandler, oas.WithErrorHandler(api.ErrorHandler))
	require.NoError(t, err)

	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	client, err := New(httpServer.URL, "secret-token", Options{})
	require.NoError(t, err)

	resp, err := client.Fetch(context.Background(), index.FetchRequest{
		URL:    "https://example.com/api",
		Method: "DELETE",
	})
	require.Error(t, err)
	assert.Empty(t, resp)
	assert.Contains(t, err.Error(), "403") // StatusForbidden
}
