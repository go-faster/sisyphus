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
