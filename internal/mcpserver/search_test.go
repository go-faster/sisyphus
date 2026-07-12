package mcpserver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/index"
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
		name           string
		args           SearchArgs
		expect         map[string]string
		expectTier     string
		expectPrefixes []string
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
		{
			name:           "source controls",
			args:           SearchArgs{Query: "test", SourceTier: "code", SourcePrefixes: []string{index.SourceGitCodePrefix}, Limit: 5},
			expect:         nil,
			expectTier:     "code",
			expectPrefixes: []string{index.SourceGitCodePrefix},
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
			assert.Equal(t, tt.expectTier, retr.got.SourceTier)
			assert.Equal(t, tt.expectPrefixes, retr.got.SourcePrefixes)
		})
	}
}

func TestAnswerHandler_Filters(t *testing.T) {
	tests := []struct {
		name           string
		args           AnswerArgs
		expect         map[string]string
		expectTier     string
		expectPrefixes []string
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
		{
			name:           "source controls",
			args:           AnswerArgs{Question: "test", SourceTier: "history", SourcePrefixes: []string{index.SourceGitCommitsPrefix}},
			expect:         nil,
			expectTier:     "history",
			expectPrefixes: []string{index.SourceGitCommitsPrefix},
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
			assert.Equal(t, tt.expectTier, retr.got.SourceTier)
			assert.Equal(t, tt.expectPrefixes, retr.got.SourcePrefixes)
		})
	}
}

func TestAnswerHandler_QueryAnswerer(t *testing.T) {
	retr := &captureRetriever{}
	answerer := &captureQueryAnswerer{}
	handler := answerHandler(retr, answerer)
	args := AnswerArgs{
		Question:       "test",
		SourceTier:     "code",
		SourcePrefixes: []string{index.SourceGitCodePrefix},
	}

	_, _, err := handler(t.Context(), nil, args)
	require.NoError(t, err)
	assert.Equal(t, "code", answerer.got.SourceTier)
	assert.Equal(t, []string{index.SourceGitCodePrefix}, answerer.got.SourcePrefixes)
}

type answerNever struct{}

func (answerNever) Answer(_ context.Context, _ index.Query, _ []index.Result) (index.Answer, error) {
	return index.Answer{Text: "stub"}, nil
}

type captureQueryAnswerer struct {
	got index.Query
}

func (a *captureQueryAnswerer) Answer(_ context.Context, q index.Query, _ []index.Result) (index.Answer, error) {
	a.got = q
	return index.Answer{Text: "stub"}, nil
}
