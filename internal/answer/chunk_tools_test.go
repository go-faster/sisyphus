package answer

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openai/openai-go/v3"
	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/index"
)

type fakeChunks struct {
	chunks map[uuid.UUID]index.Chunk
	err    error
}

func (f fakeChunks) FetchChunks(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]index.Chunk, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[uuid.UUID]index.Chunk)
	for _, id := range ids {
		if c, ok := f.chunks[id]; ok {
			out[id] = c
		}
	}
	return out, nil
}

func TestSnippet(t *testing.T) {
	for _, tt := range []struct {
		name     string
		in       string
		maxChars int
		want     string
		cut      bool
	}{
		{name: "under limit", in: "short", maxChars: 10, want: "short", cut: false},
		{name: "at limit", in: "exactly10!", maxChars: 10, want: "exactly10!", cut: false},
		{name: "over limit", in: "abcdefghijkl", maxChars: 5, want: "abcde…", cut: true},
		{name: "trims trailing space", in: "ab   cdef", maxChars: 5, want: "ab…", cut: true},
		{name: "zero disables", in: "abc", maxChars: 0, want: "abc", cut: false},
		{name: "counts runes not bytes", in: strings.Repeat("日", 10), maxChars: 3, want: "日日日…", cut: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, cut := snippet(tt.in, tt.maxChars)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.cut, cut)
		})
	}
}

// Without a ChunkFetcher there is no get_chunks to recover a body, so search
// must keep returning full text rather than snipping it away irrecoverably.
func TestSearchKnowledgeFullTextWithoutFetcher(t *testing.T) {
	body := strings.Repeat("x", 5000)
	ks := NewKnowledgeToolSource(retrieverReturning(body), fakeFetcher{}, nil, nil, nil)

	tools, err := ks.Tools(context.Background())
	require.NoError(t, err)
	require.NotContains(t, toolNames(tools), "get_chunks")

	out := searchOnce(t, ks)
	require.Equal(t, body, out[0].Text)
	require.Empty(t, out[0].Snippet)
}

func TestSearchKnowledgeSummaryMode(t *testing.T) {
	body := strings.Repeat("x", 5000)
	ks := NewKnowledgeToolSource(retrieverReturning(body), fakeFetcher{}, nil, fakeChunks{}, nil)

	tools, err := ks.Tools(context.Background())
	require.NoError(t, err)
	require.Contains(t, toolNames(tools), "get_chunks")

	out := searchOnce(t, ks)
	require.Empty(t, out[0].Text, "full body must not be inlined in summary mode")
	require.True(t, out[0].Truncated)
	require.Equal(t, 5000, out[0].TextBytes, "text_bytes reports the full size, not the snippet's")
	require.LessOrEqual(t, len([]rune(out[0].Snippet)), defaultSnippetChars+1) // +1 for the ellipsis

	// The URL survives summarisation: collectURLs reads source_url, so the
	// buttons guarantee must not depend on the full text being present.
	require.Equal(t, "https://example.com/doc", out[0].SourceURL)
	require.NotEmpty(t, out[0].ChunkID, "chunk_id is what makes the body recoverable")
}

func TestGetChunks(t *testing.T) {
	id, missing := index.NewID(), index.NewID()
	ks := NewKnowledgeToolSource(fakeRetriever{}, fakeFetcher{}, nil, fakeChunks{
		chunks: map[uuid.UUID]index.Chunk{id: {ID: id, Text: "full body"}},
	}, nil)

	raw, err := ks.Call(context.Background(), "get_chunks",
		json.RawMessage(`{"ids":["`+missing.String()+`","`+id.String()+`"]}`))
	require.NoError(t, err)

	var out []getChunksResult
	require.NoError(t, json.Unmarshal([]byte(raw), &out))
	require.Len(t, out, 2)

	// Answered in the order asked, with a missing id called out rather than dropped.
	require.Equal(t, missing.String(), out[0].ChunkID)
	require.True(t, out[0].Missing)
	require.Equal(t, id.String(), out[1].ChunkID)
	require.Equal(t, "full body", out[1].Text)
}

func TestGetChunksRejectsBadInput(t *testing.T) {
	ks := NewKnowledgeToolSource(fakeRetriever{}, fakeFetcher{}, nil, fakeChunks{}, nil)

	for _, tt := range []struct {
		name, args, wantErr string
	}{
		{name: "no ids", args: `{"ids":[]}`, wantErr: "ids is required"},
		{name: "malformed id", args: `{"ids":["not-a-uuid"]}`, wantErr: "parse chunk id"},
		{name: "too many", args: `{"ids":[` + strings.TrimSuffix(strings.Repeat(`"`+index.NewID().String()+`",`, maxGetChunksIDs+1), ",") + `]}`, wantErr: "too many ids"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ks.Call(context.Background(), "get_chunks", json.RawMessage(tt.args))
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestGetChunksUnavailableWithoutFetcher(t *testing.T) {
	ks := NewKnowledgeToolSource(fakeRetriever{}, fakeFetcher{}, nil, nil, nil)
	_, err := ks.Call(context.Background(), "get_chunks", json.RawMessage(`{"ids":["`+index.NewID().String()+`"]}`))
	require.ErrorContains(t, err, "chunk fetcher unavailable")
}

func retrieverReturning(text string) fakeRetriever {
	return fakeRetriever{results: []index.Result{{
		Chunk: index.Chunk{
			ID: index.NewID(), DocumentID: index.NewID(), Title: "Doc", Text: text,
			Type:     index.ChunkSection,
			Metadata: map[string]any{"source": "git", "source_url": "https://example.com/doc"},
		},
		Score: 0.9, Vector: true,
	}}}
}

func searchOnce(t *testing.T, ks *KnowledgeToolSource) []searchKnowledgeResult {
	t.Helper()
	raw, err := ks.Call(context.Background(), "search_knowledge", json.RawMessage(`{"query":"q"}`))
	require.NoError(t, err)
	var out []searchKnowledgeResult
	require.NoError(t, json.Unmarshal([]byte(raw), &out))
	require.Len(t, out, 1)
	return out
}

func toolNames(tools []openai.ChatCompletionToolUnionParam) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool.OfFunction != nil {
			names = append(names, tool.OfFunction.Function.Name)
		}
	}
	return names
}
