package answer

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/go-faster/errors"
	"github.com/google/uuid"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"

	"github.com/go-faster/sisyphus/internal/index"
)

// ChunkFetcher loads chunk bodies by ID. It is satisfied by
// search/postgres.Searcher, which already fetches chunks this way to hydrate
// vector hits.
type ChunkFetcher interface {
	FetchChunks(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]index.Chunk, error)
}

// Sizing for summary-mode search results.
const (
	// defaultSnippetChars bounds the preview carried by each search result.
	// A result's metadata runs ~200 bytes, so at this snippet size a 10-result
	// search costs ~5KB against the ~44KB it would cost carrying full text.
	defaultSnippetChars = 300

	// maxGetChunksIDs bounds one get_chunks call. The loop's own tool-result
	// cap is the real ceiling; this exists so a malformed call fails fast with
	// a message the model can act on rather than being silently truncated.
	maxGetChunksIDs = 20
)

type getChunksArgs struct {
	IDs []string `json:"ids"`
}

type getChunksResult struct {
	ChunkID string `json:"chunk_id"`
	Text    string `json:"text,omitempty"`
	// Missing marks an ID that matched no chunk, so a stale or invented ID is
	// visibly answered rather than silently absent from the result set.
	Missing bool `json:"missing,omitempty"`
}

func getChunksTool() openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionToolUnionParam{
		OfFunction: &openai.ChatCompletionFunctionToolParam{
			Function: openai.FunctionDefinitionParam{
				Name: "get_chunks",
				Description: openai.String("Fetch the full text of chunks returned by search_knowledge, " +
					"by their chunk_id. search_knowledge returns only a snippet of each match; call this " +
					"for the few results you actually need to read in full."),
				Parameters: shared.FunctionParameters{
					"type": "object",
					"properties": map[string]any{
						"ids": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "string"},
							"description": "chunk_id values from a previous search_knowledge result.",
						},
					},
					"required": []string{"ids"},
				},
			},
		},
	}
}

func (k *KnowledgeToolSource) getChunks(ctx context.Context, argsJSON json.RawMessage) (string, error) {
	if k.chunks == nil {
		return "", errors.New("chunk fetcher unavailable")
	}
	var args getChunksArgs
	if len(argsJSON) > 0 {
		if err := json.Unmarshal(argsJSON, &args); err != nil {
			return "", errors.Wrap(err, "unmarshal get_chunks args")
		}
	}
	if len(args.IDs) == 0 {
		return "", errors.New("ids is required")
	}
	if len(args.IDs) > maxGetChunksIDs {
		return "", errors.Errorf("too many ids: %d (max %d)", len(args.IDs), maxGetChunksIDs)
	}

	// Parse before fetching so a malformed ID is reported as such instead of
	// silently becoming a missing chunk.
	ids := make([]uuid.UUID, 0, len(args.IDs))
	for _, raw := range args.IDs {
		id, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil {
			return "", errors.Wrapf(err, "parse chunk id %q", raw)
		}
		ids = append(ids, id)
	}

	found, err := k.chunks.FetchChunks(ctx, ids)
	if err != nil {
		return "", errors.Wrap(err, "fetch chunks")
	}

	// Answer in the order asked, so the model can match results to its request
	// without re-sorting.
	out := make([]getChunksResult, 0, len(ids))
	for _, id := range ids {
		c, ok := found[id]
		if !ok {
			out = append(out, getChunksResult{ChunkID: id.String(), Missing: true})
			continue
		}
		out = append(out, getChunksResult{ChunkID: id.String(), Text: c.Text})
	}

	b, err := json.Marshal(out)
	if err != nil {
		return "", errors.Wrap(err, "marshal get_chunks result")
	}
	return string(b), nil
}

// snippet returns at most maxChars runes of text, cutting on a rune boundary
// and marking that it was shortened. It reports whether it cut anything.
func snippet(text string, maxChars int) (string, bool) {
	if maxChars <= 0 || utf8.RuneCountInString(text) <= maxChars {
		return text, false
	}
	count := 0
	for i := range text {
		if count == maxChars {
			return strings.TrimRight(text[:i], " \t\n") + "…", true
		}
		count++
	}
	return text, false
}
