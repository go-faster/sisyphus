package bot

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/index"
)

type fakeRetriever struct {
	query   index.Query
	results []index.Result
	err     error
}

func (f *fakeRetriever) Retrieve(_ context.Context, q index.Query) ([]index.Result, error) {
	f.query = q
	return f.results, f.err
}

func TestSearchResultsText(t *testing.T) {
	require.Equal(t, "No results found.", searchResultsText(nil))

	text := searchResultsText([]index.Result{
		{Chunk: index.Chunk{Title: "Runbook: prod outage", Text: "Restart the pods and check the queue depth."}},
		{Chunk: index.Chunk{Text: "no title here", Metadata: map[string]any{"source": "gitlab_issue"}}},
	})
	require.Contains(t, text, `1. **Runbook\: prod outage**`)
	require.Contains(t, text, "Restart the pods")
	require.Contains(t, text, `2. **gitlab\_issue**`)
}

func TestSearchResultsTextEscapesMarkdown(t *testing.T) {
	text := searchResultsText([]index.Result{
		{Chunk: index.Chunk{Title: "*bold* [link](evil)", Text: "snake_case_ident and # not a heading"}},
	})
	require.Contains(t, text, `\*bold\* \[link\]\(evil\)`)
	require.Contains(t, text, `snake\_case\_ident and \# not a heading`)
}

func TestSearchResultsTextCapsResultCount(t *testing.T) {
	var results []index.Result
	for i := range searchResultLimit + 2 {
		results = append(results, index.Result{Chunk: index.Chunk{Title: "result", Text: "text " + string(rune('a'+i))}})
	}

	text := searchResultsText(results)
	require.Contains(t, text, strconv.Itoa(searchResultLimit)+".")
	require.NotContains(t, text, strconv.Itoa(searchResultLimit+1)+".")
}

func TestSearchResultsTextInlinesSourceLink(t *testing.T) {
	text := searchResultsText([]index.Result{
		{Chunk: index.Chunk{
			Title:    "doc",
			Text:     "hello world",
			Metadata: map[string]any{"source_url": "https://example.com/doc"},
		}},
		{Chunk: index.Chunk{
			Title:    "invalid",
			Text:     "no link expected",
			Metadata: map[string]any{"source_url": "not-a-url"},
		}},
	})
	require.Contains(t, text, "[Source](https://example.com/doc)")
	require.NotContains(t, text, "not-a-url")
}

func TestHandleSearch(t *testing.T) {
	r := &fakeRetriever{results: []index.Result{
		{Chunk: index.Chunk{Title: "doc", Text: "hello world", Metadata: map[string]any{"source_url": "https://example.com/doc"}}},
	}}
	b := New(context.Background(), r, nil, BotCredentials{}, BotOptions{
		Silent:         true,
		TracerProvider: otel.GetTracerProvider(),
		Logger:         zap.NewNop(),
		AllowedUserIDs: []int64{1},
	})

	answer, err := b.handleSearch(context.Background(), "how to restart")
	require.NoError(t, err)
	require.Contains(t, answer.Text, "doc")
	require.Contains(t, answer.Text, "hello world")
	require.Contains(t, answer.Text, "[Source](https://example.com/doc)")
	require.Empty(t, answer.Links)
	require.Equal(t, "how to restart", r.query.Text)
	require.Equal(t, searchResultLimit, r.query.Limit)
}

func TestHandleSearchNoRetriever(t *testing.T) {
	b := New(context.Background(), nil, nil, BotCredentials{}, BotOptions{
		Silent:         true,
		TracerProvider: otel.GetTracerProvider(),
		Logger:         zap.NewNop(),
		AllowedUserIDs: []int64{1},
	})

	_, err := b.handleSearch(context.Background(), "query")
	require.Error(t, err)
}
