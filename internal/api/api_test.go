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

func TestHandler_Search_Filters(t *testing.T) {
	tests := []struct {
		name   string
		req    *oas.SearchRequest
		expect map[string]string
	}{
		{
			name: "no filters",
			req: &oas.SearchRequest{
				Query:   "test",
				Service: oas.NewOptString(""),
				Limit:   oas.NewOptInt32(5),
			},
			expect: nil,
		},
		{
			name: "single filter",
			req: &oas.SearchRequest{
				Query:   "test",
				Service: oas.NewOptString(""),
				Limit:   oas.NewOptInt32(5),
				Filters: oas.NewOptSearchRequestFilters(oas.SearchRequestFilters{"status": "In Review"}),
			},
			expect: map[string]string{"status": "In Review"},
		},
		{
			name: "multiple filters",
			req: &oas.SearchRequest{
				Query:   "test",
				Service: oas.NewOptString(""),
				Limit:   oas.NewOptInt32(5),
				Filters: oas.NewOptSearchRequestFilters(oas.SearchRequestFilters{"jira_key": "BILL-42", "status": "In Review"}),
			},
			expect: map[string]string{"jira_key": "BILL-42", "status": "In Review"},
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
		})
	}
}

func TestHandler_Context_Filters(t *testing.T) {
	tests := []struct {
		name   string
		req    *oas.ContextRequest
		expect map[string]string
	}{
		{
			name: "no filters",
			req: &oas.ContextRequest{
				Question: "test",
				Service:  oas.NewOptString(""),
			},
			expect: nil,
		},
		{
			name: "single filter",
			req: &oas.ContextRequest{
				Question: "test",
				Service:  oas.NewOptString(""),
				Filters:  oas.NewOptContextRequestFilters(oas.ContextRequestFilters{"status": "In Review"}),
			},
			expect: map[string]string{"status": "In Review"},
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
		})
	}
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
