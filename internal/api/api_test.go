package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/go-faster/errors"
	"github.com/ogen-go/ogen/ogenerrors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/index"
	"github.com/go-faster/sisyphus/internal/oas"
)

type captureRetriever struct {
	got index.Query
}

func (f *captureRetriever) Retrieve(ctx context.Context, q index.Query) ([]index.Result, error) {
	f.got = q
	return nil, nil
}

type stubAnswerer struct{}

func (stubAnswerer) Answer(_ context.Context, _ string, _ []index.Result) (string, error) {
	return "stub", nil
}

type captureAnswerIndexer struct {
	doc index.Document
}

func (c *captureAnswerIndexer) Index(_ context.Context, doc index.Document) error {
	c.doc = doc
	return nil
}

func TestHandler_GetHealth(t *testing.T) {
	h := New(&captureRetriever{}, stubAnswerer{}, "test")

	resp, err := h.GetHealth(t.Context())
	require.NoError(t, err)
	require.Equal(t, "ok", resp.Status)
	require.Equal(t, "test", resp.Version.Value)
}

func TestHandler_Search_Filters(t *testing.T) {
	tests := []struct {
		name           string
		req            *oas.SearchRequest
		expect         map[string]string
		expectPrefixes []string
	}{
		{
			name: "no filters",
			req: &oas.SearchRequest{
				Query:   "test",
				Service: oas.NewOptString(""),
				Limit:   oas.NewOptInt32(5),
			},
			expect:         nil,
			expectPrefixes: sourceTierPrefixes[sourceTierCurated],
		},
		{
			name: "single filter",
			req: &oas.SearchRequest{
				Query:   "test",
				Service: oas.NewOptString(""),
				Limit:   oas.NewOptInt32(5),
				Filters: oas.NewOptSearchRequestFilters(oas.SearchRequestFilters{"status": "In Review"}),
			},
			expect:         map[string]string{"status": "In Review"},
			expectPrefixes: sourceTierPrefixes[sourceTierCurated],
		},
		{
			name: "multiple filters",
			req: &oas.SearchRequest{
				Query:   "test",
				Service: oas.NewOptString(""),
				Limit:   oas.NewOptInt32(5),
				Filters: oas.NewOptSearchRequestFilters(oas.SearchRequestFilters{"jira_key": "BILL-42", "status": "In Review"}),
			},
			expect:         map[string]string{"jira_key": "BILL-42", "status": "In Review"},
			expectPrefixes: sourceTierPrefixes[sourceTierCurated],
		},
		{
			name: "code tier",
			req: &oas.SearchRequest{
				Query:      "test",
				SourceTier: oas.NewOptString(sourceTierCode),
			},
			expect:         nil,
			expectPrefixes: sourceTierPrefixes[sourceTierCode],
		},
		{
			name: "explicit source filter disables tier",
			req: &oas.SearchRequest{
				Query:      "test",
				Filters:    oas.NewOptSearchRequestFilters(oas.SearchRequestFilters{"source": "git_code:repo"}),
				SourceTier: oas.NewOptString(sourceTierCode),
			},
			expect:         map[string]string{"source": "git_code:repo"},
			expectPrefixes: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			retr := &captureRetriever{}
			h := New(retr, stubAnswerer{}, "test")
			ctx := t.Context()
			_, err := h.Search(ctx, tt.req)
			require.NoError(t, err)
			if tt.expect == nil {
				assert.Nil(t, retr.got.Filters)
			} else {
				assert.Equal(t, tt.expect, retr.got.Filters)
			}
			assert.Equal(t, tt.expectPrefixes, retr.got.SourcePrefixes)
		})
	}
}

func TestHandler_Context_Filters(t *testing.T) {
	tests := []struct {
		name           string
		req            *oas.ContextRequest
		expect         map[string]string
		expectPrefixes []string
	}{
		{
			name: "no filters",
			req: &oas.ContextRequest{
				Question: "test",
				Service:  oas.NewOptString(""),
			},
			expect:         nil,
			expectPrefixes: sourceTierPrefixes[sourceTierCurated],
		},
		{
			name: "single filter",
			req: &oas.ContextRequest{
				Question: "test",
				Service:  oas.NewOptString(""),
				Filters:  oas.NewOptContextRequestFilters(oas.ContextRequestFilters{"status": "In Review"}),
			},
			expect:         map[string]string{"status": "In Review"},
			expectPrefixes: sourceTierPrefixes[sourceTierCurated],
		},
		{
			name: "explicit prefixes",
			req: &oas.ContextRequest{
				Question:       "test",
				SourcePrefixes: []string{index.SourceGitCodePrefix},
			},
			expect:         nil,
			expectPrefixes: []string{index.SourceGitCodePrefix},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			retr := &captureRetriever{}
			h := New(retr, stubAnswerer{}, "test")
			ctx := t.Context()
			_, err := h.Context(ctx, tt.req)
			require.NoError(t, err)
			if tt.expect == nil {
				assert.Nil(t, retr.got.Filters)
			} else {
				assert.Equal(t, tt.expect, retr.got.Filters)
			}
			assert.Equal(t, tt.expectPrefixes, retr.got.SourcePrefixes)
		})
	}
}

func TestHandler_Context_IndexesAnsweredQuestion(t *testing.T) {
	idx := &captureAnswerIndexer{}
	h := New(&captureRetriever{}, stubAnswerer{}, "test", WithAnswerIndexer(idx))

	_, err := h.Context(t.Context(), &oas.ContextRequest{Question: "How to deploy?"})
	require.NoError(t, err)
	require.Equal(t, index.SourceAnswer, idx.doc.Source)
	require.Equal(t, index.Hash("How to deploy?"), idx.doc.SourceID)
	require.Equal(t, "How to deploy?", idx.doc.Title)
	require.Contains(t, idx.doc.Body, "# How to deploy?")
	require.Contains(t, idx.doc.Body, "## Answer")
	require.Equal(t, string(index.SourceAnswer), idx.doc.Metadata["source"])
	require.Equal(t, string(index.AuthorityLow), idx.doc.Metadata["authority"])
}

type richAnswerer struct{}

func (richAnswerer) Answer(_ context.Context, _ string, _ []index.Result) (string, error) {
	return "text", nil
}

func (richAnswerer) AnswerRich(_ context.Context, _ string, _ []index.Result) (index.Answer, error) {
	return index.Answer{
		Text:  "text",
		Links: []index.Link{{Text: "Dashboard", URL: "https://grafana/d/1"}},
	}, nil
}

func TestHandler_Context_RichAnswererButtons(t *testing.T) {
	h := New(&captureRetriever{}, richAnswerer{}, "test")

	resp, err := h.Context(t.Context(), &oas.ContextRequest{Question: "why red?"})
	require.NoError(t, err)
	require.Equal(t, "text", resp.Answer)
	require.Equal(t, []oas.Link{{Text: "Dashboard", URL: "https://grafana/d/1"}}, resp.Buttons)
}

func TestHandler_Context_PlainAnswererNoButtons(t *testing.T) {
	h := New(&captureRetriever{}, stubAnswerer{}, "test")

	resp, err := h.Context(t.Context(), &oas.ContextRequest{Question: "why red?"})
	require.NoError(t, err)
	require.Equal(t, "stub", resp.Answer)
	require.Empty(t, resp.Buttons)
}

func TestHandler_NewError_GenericError(t *testing.T) {
	h := New(&captureRetriever{}, stubAnswerer{}, "test")
	ctx := t.Context()

	// Test that a generic error returns a generic message without leaking the raw error.
	dbErr := errors.New("database connection failed")
	errResp := h.NewError(ctx, dbErr)

	require.NotNil(t, errResp)
	assert.Equal(t, http.StatusInternalServerError, errResp.StatusCode)
	assert.Equal(t, "internal server error", errResp.Response.ErrorMessage)
	// Verify the raw error message is not in the response.
	assert.NotEqual(t, "database connection failed", errResp.Response.ErrorMessage)
}

func TestHandler_NewError_SecurityError(t *testing.T) {
	h := New(&captureRetriever{}, stubAnswerer{}, "test")
	ctx := t.Context()

	// Test that a security error still returns 401 "unauthorized".
	secErr := &ogenerrors.SecurityError{
		Err: errors.New("invalid token"),
	}
	errResp := h.NewError(ctx, secErr)

	require.NotNil(t, errResp)
	assert.Equal(t, http.StatusUnauthorized, errResp.StatusCode)
	assert.Equal(t, "unauthorized", errResp.Response.ErrorMessage)
}

// fakeContentResolver provides test responses for GetFile.
type fakeContentResolver struct {
	response index.ContentResponse
	err      error
}

func (f *fakeContentResolver) ResolveContent(ctx context.Context, req index.ContentRequest) (index.ContentResponse, error) {
	return f.response, f.err
}

// fakeURLFetcher provides test responses for FetchURL.
type fakeURLFetcher struct {
	response index.FetchResponse
	err      error
}

func (f *fakeURLFetcher) Fetch(ctx context.Context, req index.FetchRequest) (index.FetchResponse, error) {
	return f.response, f.err
}

func TestHandler_GetFile_NotConfigured(t *testing.T) {
	h := New(&captureRetriever{}, stubAnswerer{}, "test")

	resp, err := h.GetFile(t.Context(), &oas.FileRequest{
		Repo: "myrepo",
		Path: "README.md",
	})
	require.NoError(t, err)
	assert.False(t, resp.Found)
	assert.Empty(t, resp.Content)
	assert.Empty(t, resp.Source.Value)
}

func TestHandler_GetFile_Found(t *testing.T) {
	resolver := &fakeContentResolver{
		response: index.ContentResponse{
			Content: "# Hello\n\nWorld",
			Source:  "database",
			Found:   true,
		},
	}
	h := New(&captureRetriever{}, stubAnswerer{}, "test", WithContentResolver(resolver))

	resp, err := h.GetFile(t.Context(), &oas.FileRequest{
		Repo: "myrepo",
		Path: "README.md",
	})
	require.NoError(t, err)
	assert.True(t, resp.Found)
	assert.Equal(t, "# Hello\n\nWorld", resp.Content)
	assert.Equal(t, "database", resp.Source.Value)
}

func TestHandler_GetFile_NotFound(t *testing.T) {
	resolver := &fakeContentResolver{
		response: index.ContentResponse{
			Found: false,
		},
	}
	h := New(&captureRetriever{}, stubAnswerer{}, "test", WithContentResolver(resolver))

	resp, err := h.GetFile(t.Context(), &oas.FileRequest{
		Repo: "myrepo",
		Path: "nonexistent.go",
	})
	require.NoError(t, err)
	assert.False(t, resp.Found)
	assert.Empty(t, resp.Content)
}

func TestHandler_GetFile_ResolveError(t *testing.T) {
	resolver := &fakeContentResolver{
		err: errors.New("database error"),
	}
	h := New(&captureRetriever{}, stubAnswerer{}, "test", WithContentResolver(resolver))

	resp, err := h.GetFile(t.Context(), &oas.FileRequest{
		Repo: "myrepo",
		Path: "README.md",
	})
	require.NoError(t, err)
	assert.False(t, resp.Found)
	assert.Empty(t, resp.Content)
}

func TestHandler_GetFile_WithLineRange(t *testing.T) {
	resolver := &fakeContentResolver{
		response: index.ContentResponse{
			Content: "line 1\nline 2\nline 3",
			Source:  "local_clone",
			Found:   true,
		},
	}
	h := New(&captureRetriever{}, stubAnswerer{}, "test", WithContentResolver(resolver))

	resp, err := h.GetFile(t.Context(), &oas.FileRequest{
		Repo:   "myrepo",
		Path:   "code.go",
		Start:  oas.NewOptInt32(1),
		End:    oas.NewOptInt32(2),
		Branch: oas.NewOptString("main"),
	})
	require.NoError(t, err)
	assert.True(t, resp.Found)
	assert.Equal(t, "line 1\nline 2\nline 3", resp.Content)
	assert.Equal(t, "local_clone", resp.Source.Value)
}

func TestHandler_FetchURL_NotConfigured(t *testing.T) {
	h := New(&captureRetriever{}, stubAnswerer{}, "test")

	resp, err := h.FetchURL(t.Context(), &oas.FetchURLRequest{
		URL: "https://example.com/data",
	})
	require.Error(t, err)
	assert.Nil(t, resp)
	errResp, ok := err.(*oas.ErrorStatusCode)
	require.True(t, ok)
	assert.Equal(t, http.StatusForbidden, errResp.StatusCode)
	assert.Equal(t, "url fetcher not configured", errResp.Response.ErrorMessage)
}

func TestHandler_FetchURL_Success(t *testing.T) {
	fetcher := &fakeURLFetcher{
		response: index.FetchResponse{
			StatusCode: 200,
			Body:       `{"data":"value"}`,
			FromSite:   "example.com",
			Truncated:  false,
			Headers: map[string]string{
				"Content-Type": "application/json",
				"X-Custom":     "header",
			},
		},
	}
	h := New(&captureRetriever{}, stubAnswerer{}, "test", WithURLFetcher(fetcher))

	resp, err := h.FetchURL(t.Context(), &oas.FetchURLRequest{
		URL:    "https://example.com/data",
		Method: oas.NewOptString("GET"),
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, `{"data":"value"}`, resp.Body)
	assert.Equal(t, "example.com", resp.FromSite)
	assert.False(t, resp.Truncated.Value)
	// Headers should be present
	assert.NotNil(t, resp.Headers)
	require.True(t, resp.Headers.Set)
	assert.Equal(t, "application/json", resp.Headers.Value["Content-Type"])
	assert.Equal(t, "header", resp.Headers.Value["X-Custom"])
}

func TestHandler_FetchURL_NoHeaders(t *testing.T) {
	fetcher := &fakeURLFetcher{
		response: index.FetchResponse{
			StatusCode: 204,
			Body:       "",
			FromSite:   "api.example.com",
			Truncated:  false,
			Headers:    nil, // No headers
		},
	}
	h := New(&captureRetriever{}, stubAnswerer{}, "test", WithURLFetcher(fetcher))

	resp, err := h.FetchURL(t.Context(), &oas.FetchURLRequest{
		URL: "https://api.example.com/endpoint",
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, 204, resp.StatusCode)
	// Headers should not be set
	assert.False(t, resp.Headers.Set)
}

func TestHandler_FetchURL_URLNotAllowed(t *testing.T) {
	fetcher := &fakeURLFetcher{
		err: index.ErrURLNotAllowed,
	}
	h := New(&captureRetriever{}, stubAnswerer{}, "test", WithURLFetcher(fetcher))

	resp, err := h.FetchURL(t.Context(), &oas.FetchURLRequest{
		URL: "https://forbidden.com/data",
	})
	require.Error(t, err)
	assert.Nil(t, resp)
	errResp, ok := err.(*oas.ErrorStatusCode)
	require.True(t, ok)
	assert.Equal(t, http.StatusForbidden, errResp.StatusCode)
	assert.Equal(t, "url not in allowlist", errResp.Response.ErrorMessage)
}

func TestHandler_FetchURL_MethodNotAllowed(t *testing.T) {
	fetcher := &fakeURLFetcher{
		err: index.ErrFetchMethodNotAllowed,
	}
	h := New(&captureRetriever{}, stubAnswerer{}, "test", WithURLFetcher(fetcher))

	resp, err := h.FetchURL(t.Context(), &oas.FetchURLRequest{
		URL:    "https://example.com/api",
		Method: oas.NewOptString("DELETE"),
	})
	require.Error(t, err)
	assert.Nil(t, resp)
	errResp, ok := err.(*oas.ErrorStatusCode)
	require.True(t, ok)
	assert.Equal(t, http.StatusForbidden, errResp.StatusCode)
	assert.Equal(t, "method not allowed for site", errResp.Response.ErrorMessage)
}

func TestHandler_FetchURL_GenericError(t *testing.T) {
	fetcher := &fakeURLFetcher{
		err: errors.New("network timeout"),
	}
	h := New(&captureRetriever{}, stubAnswerer{}, "test", WithURLFetcher(fetcher))

	resp, err := h.FetchURL(t.Context(), &oas.FetchURLRequest{
		URL: "https://example.com/data",
	})
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "fetch url")
}

func TestHandler_FetchURL_WithBody(t *testing.T) {
	fetcher := &fakeURLFetcher{
		response: index.FetchResponse{
			StatusCode: 200,
			Body:       `{"result":"ok"}`,
			FromSite:   "api.example.com",
			Truncated:  true,
			Headers:    map[string]string{"X-Truncated": "true"},
		},
	}
	h := New(&captureRetriever{}, stubAnswerer{}, "test", WithURLFetcher(fetcher))

	resp, err := h.FetchURL(t.Context(), &oas.FetchURLRequest{
		URL:    "https://api.example.com/endpoint",
		Method: oas.NewOptString("POST"),
		Body:   oas.NewOptString(`{"key":"value"}`),
		Headers: oas.NewOptFetchURLRequestHeaders(map[string]string{
			"Content-Type": "application/json",
		}),
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, `{"result":"ok"}`, resp.Body)
	assert.True(t, resp.Truncated.Value)
}
