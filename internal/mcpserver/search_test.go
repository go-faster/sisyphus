package mcpserver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-faster/scpbot/internal/index"
)

type captureRetriever struct {
	got index.Query
}

func (f *captureRetriever) Retrieve(ctx context.Context, q index.Query) ([]index.Result, error) {
	f.got = q
	return nil, nil
}

func TestSearchHandler_Filters(t *testing.T) {
	tests := []struct {
		name   string
		args   SearchArgs
		expect map[string]string
	}{
		{
			name:   "no filters",
			args:   SearchArgs{Query: "test", Limit: 5},
			expect: nil,
		},
		{
			name:   "single filter",
			args:   SearchArgs{Query: "test", Filters: map[string]string{"status": "In Review"}, Limit: 5},
			expect: map[string]string{"status": "In Review"},
		},
		{
			name:   "multiple filters",
			args:   SearchArgs{Query: "test", Filters: map[string]string{"jira_key": "BILL-42", "status": "In Review"}, Limit: 5},
			expect: map[string]string{"jira_key": "BILL-42", "status": "In Review"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			retr := &captureRetriever{}
			handler := searchHandler(retr)
			ctx := t.Context()
			_, _, err := handler(ctx, nil, tt.args)
			require.NoError(t, err)
			if tt.expect == nil {
				assert.Nil(t, retr.got.Filters)
			} else {
				assert.Equal(t, tt.expect, retr.got.Filters)
			}
		})
	}
}

func TestAnswerHandler_Filters(t *testing.T) {
	tests := []struct {
		name   string
		args   AnswerArgs
		expect map[string]string
	}{
		{
			name:   "no filters",
			args:   AnswerArgs{Question: "test"},
			expect: nil,
		},
		{
			name:   "single filter",
			args:   AnswerArgs{Question: "test", Filters: map[string]string{"status": "In Review"}},
			expect: map[string]string{"status": "In Review"},
		},
		{
			name:   "multiple filters",
			args:   AnswerArgs{Question: "test", Filters: map[string]string{"jira_key": "BILL-42", "status": "In Review"}},
			expect: map[string]string{"jira_key": "BILL-42", "status": "In Review"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			retr := &captureRetriever{}
			handler := answerHandler(retr, answerNever{})
			ctx := t.Context()
			_, _, err := handler(ctx, nil, tt.args)
			require.NoError(t, err)
			if tt.expect == nil {
				assert.Nil(t, retr.got.Filters)
			} else {
				assert.Equal(t, tt.expect, retr.got.Filters)
			}
		})
	}
}

type answerNever struct{}

func (answerNever) Answer(_ context.Context, _ string, _ []index.Result) (string, error) {
	return "stub", nil
}
