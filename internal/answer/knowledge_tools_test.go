package answer

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/index"
)

type fakeRetriever struct{ results []index.Result }

func (f fakeRetriever) Retrieve(ctx context.Context, q index.Query) ([]index.Result, error) {
	return f.results, nil
}

type fakeFetcher struct{ resp index.FetchResponse }

func (f fakeFetcher) Fetch(ctx context.Context, req index.FetchRequest) (index.FetchResponse, error) {
	return f.resp, nil
}

func TestKnowledgeToolSource_SearchKnowledge(t *testing.T) {
	ks := NewKnowledgeToolSource(fakeRetriever{results: []index.Result{{Chunk: index.Chunk{ID: index.NewID(), DocumentID: index.NewID(), Title: "Doc", Text: "body", Type: index.ChunkSection, Metadata: map[string]any{"source": "git", "source_url": "https://example.com/doc"}}, Score: 0.9, Vector: true}}}, fakeFetcher{}, nil)
	got, err := ks.Call(context.Background(), "search_knowledge", json.RawMessage(`{"query":"hello","limit":1}`))
	require.NoError(t, err)
	require.Contains(t, got, `"chunk_id"`)
	require.Contains(t, got, `"source_url":"https://example.com/doc"`)
}

func TestKnowledgeToolSource_FetchURL(t *testing.T) {
	ks := NewKnowledgeToolSource(fakeRetriever{}, fakeFetcher{resp: index.FetchResponse{StatusCode: 200, Body: "ok", FromSite: "site", Truncated: false, Headers: map[string]string{"Content-Type": "text/plain"}}}, nil)
	got, err := ks.Call(context.Background(), "fetch_url", json.RawMessage(`{"url":"https://example.com"}`))
	require.NoError(t, err)
	require.Contains(t, got, `"status_code":200`)
	require.Contains(t, got, `"body":"ok"`)
}

func TestKnowledgeToolSource_UnknownTool(t *testing.T) {
	ks := NewKnowledgeToolSource(fakeRetriever{}, fakeFetcher{}, nil)
	_, err := ks.Call(context.Background(), "nope", nil)
	require.Error(t, err)
}

func TestKnowledgeToolSource_Tools(t *testing.T) {
	ks := NewKnowledgeToolSource(fakeRetriever{}, fakeFetcher{}, nil)
	tools, err := ks.Tools(context.Background())
	require.NoError(t, err)
	require.Len(t, tools, 2)
	require.Equal(t, "search_knowledge", tools[0].OfFunction.Function.Name)
	require.Equal(t, "fetch_url", tools[1].OfFunction.Function.Name)
}
