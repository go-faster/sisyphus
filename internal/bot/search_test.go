package bot

import (
	"context"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/gotd/td/tg"
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
	require.Contains(t, text, "```\nRestart the pods")
	require.Contains(t, text, `2. **gitlab\_issue**`)
}

func TestSearchResultsTextEscapesMarkdown(t *testing.T) {
	text := searchResultsText([]index.Result{
		{Chunk: index.Chunk{Title: "*bold* [link](evil)", Text: "snake_case_ident and # not a heading"}},
	})
	require.Contains(t, text, `\*bold\* \[link\]\(evil\)`)
	// Snippet is now inside a fenced code block, so no escaping needed.
	require.Contains(t, text, "```\nsnake_case_ident and # not a heading\n```")
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

func TestFormatSearchResultRendersCodeBlockEntity(t *testing.T) {
	r := index.Result{
		Chunk: index.Chunk{Title: "test", Text: "some code or data"},
		Score: 1.0,
	}
	md := formatSearchResult(r, 0)
	text, entities := render(t, md)
	require.Contains(t, text, "some code or data")
	hasPre := false
	for _, e := range entities {
		if _, ok := e.(*tg.MessageEntityPre); ok {
			hasPre = true
			break
		}
	}
	require.True(t, hasPre, "expected a MessageEntityPre for the code block")
}

func TestDedupResults(t *testing.T) {
	doc1 := uuid.New()
	doc2 := uuid.New()
	results := []index.Result{
		{Chunk: index.Chunk{DocumentID: doc1, Title: "doc1a"}, Score: 0.9},
		{Chunk: index.Chunk{DocumentID: doc2, Title: "doc2"}, Score: 0.8},
		{Chunk: index.Chunk{DocumentID: doc1, Title: "doc1b"}, Score: 0.7},
	}
	deduped := dedupResults(results, 2)
	require.Len(t, deduped, 2)
	require.Equal(t, "doc1a", deduped[0].Chunk.Title)
	require.Equal(t, "doc2", deduped[1].Chunk.Title)
}

func TestDedupResultsEmpty(t *testing.T) {
	require.Nil(t, dedupResults(nil, 5))
}

func TestDedupResultsCaps(t *testing.T) {
	results := make([]index.Result, 10)
	for i := range results {
		results[i] = index.Result{
			Chunk: index.Chunk{
				DocumentID: uuid.New(),
				Title:      strconv.Itoa(i),
			},
		}
	}
	require.Len(t, dedupResults(results, 3), 3)
}

func TestHandleSearch(t *testing.T) {
	r := &fakeRetriever{results: []index.Result{
		{Chunk: index.Chunk{
			ID: uuid.New(), DocumentID: uuid.New(),
			Title: "doc", Text: "hello world",
			Metadata: map[string]any{"source_url": "https://example.com/doc"},
		}},
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
	require.Equal(t, searchPool, r.query.Limit)
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

func TestSearchInlineResults(t *testing.T) {
	results := []index.Result{
		{
			Chunk: index.Chunk{
				ID: uuid.New(), DocumentID: uuid.New(),
				Title: "Test Doc 1",
				Text:  "Some content here for testing",
			},
			Score: 0.9,
		},
		{
			Chunk: index.Chunk{
				ID: uuid.New(), DocumentID: uuid.New(),
				Title: "",
				Text:  "No title, using source fallback",
				Metadata: map[string]any{
					"source":     "gitlab_issue",
					"source_url": "https://gitlab.com/test/1",
				},
			},
			Score: 0.8,
		},
		{
			Chunk: index.Chunk{
				ID: uuid.New(), DocumentID: uuid.New(),
				Title: "With URL",
				Text:  "Has a source URL",
				Metadata: map[string]any{
					"source_url": "https://example.com/doc",
				},
			},
			Score: 0.7,
		},
	}
	opts := searchInlineResults(results)
	require.Len(t, opts, 3)
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
