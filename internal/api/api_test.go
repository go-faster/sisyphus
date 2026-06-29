package api

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-faster/scpbot/internal/index"
	"github.com/go-faster/scpbot/internal/oas"
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
