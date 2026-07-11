// Package answer implements the agentic /context workflow.
package answer

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/go-faster/errors"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/go-faster/sisyphus/internal/index"
)

// Retriever is the minimal retrieval interface used by the knowledge tools.
type Retriever interface {
	Retrieve(ctx context.Context, q index.Query) ([]index.Result, error)
}

// KnowledgeToolSource implements agent.ToolSource by wrapping retrieval and URL fetch.
type KnowledgeToolSource struct {
	retriever Retriever
	fetcher   index.URLFetcher
	tracer    trace.Tracer
}

func NewKnowledgeToolSource(retriever Retriever, fetcher index.URLFetcher, tracer trace.Tracer) *KnowledgeToolSource {
	if tracer == nil {
		tracer = noop.NewTracerProvider().Tracer("github.com/go-faster/sisyphus/answer")
	}
	return &KnowledgeToolSource{retriever: retriever, fetcher: fetcher, tracer: tracer}
}

func (k *KnowledgeToolSource) Tools(_ context.Context) ([]openai.ChatCompletionToolUnionParam, error) {
	return []openai.ChatCompletionToolUnionParam{
		searchKnowledgeTool(),
		fetchURLTool(),
	}, nil
}

func (k *KnowledgeToolSource) Call(ctx context.Context, name string, argsJSON json.RawMessage) (string, error) {
	switch name {
	case "search_knowledge":
		return k.searchKnowledge(ctx, argsJSON)
	case "fetch_url":
		return k.fetchURL(ctx, argsJSON)
	default:
		return "", errors.Errorf("unknown tool %q", name)
	}
}

type searchKnowledgeArgs struct {
	Query          string            `json:"query"`
	Service        string            `json:"service"`
	Filters        map[string]string `json:"filters"`
	SourceTier     string            `json:"source_tier"`
	SourcePrefixes []string          `json:"source_prefixes"`
	Limit          int               `json:"limit"`
}

type searchKnowledgeResult struct {
	ChunkID    string  `json:"chunk_id"`
	DocumentID string  `json:"document_id"`
	Source     string  `json:"source"`
	SourceURL  string  `json:"source_url,omitempty"`
	Title      string  `json:"title,omitempty"`
	ChunkType  string  `json:"chunk_type"`
	Text       string  `json:"text"`
	Score      float64 `json:"score"`
	Vector     bool    `json:"vector"`
}

func searchKnowledgeTool() openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionToolUnionParam{
		OfFunction: &openai.ChatCompletionFunctionToolParam{
			Function: openai.FunctionDefinitionParam{
				Name:        "search_knowledge",
				Description: openai.String("Search the knowledge base with hybrid lexical and vector retrieval."),
				Parameters: shared.FunctionParameters{
					"type": "object",
					"properties": map[string]any{
						"query":   map[string]any{"type": "string"},
						"service": map[string]any{"type": "string"},
						"filters": map[string]any{
							"type":                 "object",
							"additionalProperties": map[string]any{"type": "string"},
						},
						"source_tier": map[string]any{"type": "string"},
						"source_prefixes": map[string]any{
							"type":  "array",
							"items": map[string]any{"type": "string"},
						},
						"limit": map[string]any{"type": "integer", "default": 30},
					},
					"required": []string{"query"},
				},
			},
		},
	}
}

func (k *KnowledgeToolSource) searchKnowledge(ctx context.Context, argsJSON json.RawMessage) (string, error) {
	if k.retriever == nil {
		return "", errors.New("retriever unavailable")
	}
	var args searchKnowledgeArgs
	if len(argsJSON) > 0 {
		if err := json.Unmarshal(argsJSON, &args); err != nil {
			return "", errors.Wrap(err, "unmarshal search_knowledge args")
		}
	}
	if args.Limit <= 0 {
		args.Limit = 30
	}
	results, err := k.retriever.Retrieve(ctx, index.Query{
		Text:           strings.TrimSpace(args.Query),
		Service:        strings.TrimSpace(args.Service),
		Filters:        args.Filters,
		SourceTier:     strings.TrimSpace(args.SourceTier),
		SourcePrefixes: args.SourcePrefixes,
		Limit:          args.Limit,
	})
	if err != nil {
		return "", errors.Wrap(err, "retrieve")
	}
	out := make([]searchKnowledgeResult, 0, len(results))
	for _, r := range results {
		out = append(out, searchKnowledgeResult{
			ChunkID:    r.Chunk.ID.String(),
			DocumentID: r.Chunk.DocumentID.String(),
			Source:     metaString(r.Chunk.Metadata, "source"),
			SourceURL:  metaString(r.Chunk.Metadata, "source_url"),
			Title:      r.Chunk.Title,
			ChunkType:  string(r.Chunk.Type),
			Text:       r.Chunk.Text,
			Score:      r.Score,
			Vector:     r.Vector,
		})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", errors.Wrap(err, "marshal search_knowledge result")
	}
	return string(b), nil
}

type fetchURLArgs struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Body    string            `json:"body"`
	Headers map[string]string `json:"headers"`
}

type fetchURLResult struct {
	StatusCode int               `json:"status_code"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
	FromSite   string            `json:"from_site"`
	Truncated  bool              `json:"truncated"`
}

func fetchURLTool() openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionToolUnionParam{
		OfFunction: &openai.ChatCompletionFunctionToolParam{
			Function: openai.FunctionDefinitionParam{
				Name:        "fetch_url",
				Description: openai.String("Fetch content from an operator-approved URL allowlist."),
				Parameters: shared.FunctionParameters{
					"type": "object",
					"properties": map[string]any{
						"url":    map[string]any{"type": "string"},
						"method": map[string]any{"type": "string", "default": "GET"},
						"body":   map[string]any{"type": "string"},
						"headers": map[string]any{
							"type":                 "object",
							"additionalProperties": map[string]any{"type": "string"},
						},
					},
					"required": []string{"url"},
				},
			},
		},
	}
}

func (k *KnowledgeToolSource) fetchURL(ctx context.Context, argsJSON json.RawMessage) (string, error) {
	if k.fetcher == nil {
		return "", errors.New("fetcher unavailable")
	}
	var args fetchURLArgs
	if len(argsJSON) > 0 {
		if err := json.Unmarshal(argsJSON, &args); err != nil {
			return "", errors.Wrap(err, "unmarshal fetch_url args")
		}
	}
	resp, err := k.fetcher.Fetch(ctx, index.FetchRequest{
		URL:     strings.TrimSpace(args.URL),
		Method:  strings.TrimSpace(args.Method),
		Body:    args.Body,
		Headers: args.Headers,
	})
	if err != nil {
		return "", errors.Wrap(err, "fetch")
	}
	b, err := json.Marshal(fetchURLResult{
		StatusCode: resp.StatusCode,
		Headers:    resp.Headers,
		Body:       resp.Body,
		FromSite:   resp.FromSite,
		Truncated:  resp.Truncated,
	})
	if err != nil {
		return "", errors.Wrap(err, "marshal fetch_url result")
	}
	return string(b), nil
}

var _ interface {
	Tools(context.Context) ([]openai.ChatCompletionToolUnionParam, error)
	Call(context.Context, string, json.RawMessage) (string, error)
} = (*KnowledgeToolSource)(nil)
