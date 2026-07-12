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

// KnowledgeToolSource implements agent.ToolSource by wrapping retrieval, URL
// fetch, and file content resolution.
type KnowledgeToolSource struct {
	retriever Retriever
	fetcher   index.URLFetcher
	resolver  index.ContentResolver
	tracer    trace.Tracer
}

func NewKnowledgeToolSource(retriever Retriever, fetcher index.URLFetcher, resolver index.ContentResolver, tracer trace.Tracer) *KnowledgeToolSource {
	if tracer == nil {
		tracer = noop.NewTracerProvider().Tracer("github.com/go-faster/sisyphus/internal/answer")
	}
	return &KnowledgeToolSource{retriever: retriever, fetcher: fetcher, resolver: resolver, tracer: tracer}
}

func (k *KnowledgeToolSource) Tools(_ context.Context) ([]openai.ChatCompletionToolUnionParam, error) {
	tools := []openai.ChatCompletionToolUnionParam{
		searchKnowledgeTool(),
		fetchURLTool(),
	}
	if k.resolver != nil {
		tools = append(tools, getFileContentTool())
	}
	return tools, nil
}

func (k *KnowledgeToolSource) Call(ctx context.Context, name string, argsJSON json.RawMessage) (string, error) {
	switch name {
	case "search_knowledge":
		return k.searchKnowledge(ctx, argsJSON)
	case "fetch_url":
		return k.fetchURL(ctx, argsJSON)
	case "get_file_content":
		return k.getFileContent(ctx, argsJSON)
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
				Name: "search_knowledge",
				Description: openai.String("Search the knowledge base with hybrid lexical and vector retrieval. " +
					"Use for fuzzy discovery when you don't already know the exact repo/path to look at. " +
					"If you already know the file, prefer get_file_content for its exact, full/current content."),
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

type getFileContentArgs struct {
	Repo   string `json:"repo"`
	Path   string `json:"path"`
	Branch string `json:"branch"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
}

type getFileContentResult struct {
	Content string `json:"content"`
	Source  string `json:"source,omitempty"`
	Found   bool   `json:"found"`
}

func getFileContentTool() openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionToolUnionParam{
		OfFunction: &openai.ChatCompletionFunctionToolParam{
			Function: openai.FunctionDefinitionParam{
				Name: "get_file_content",
				Description: openai.String("Retrieve the exact, full (or line-ranged) content of a known file from an " +
					"ingested repository. Use this instead of search_knowledge when you already know the repo and " +
					"path (e.g. from a search_knowledge result's metadata or a prior tool result) and need the " +
					"file's real current content rather than a retrieved chunk."),
				Parameters: shared.FunctionParameters{
					"type": "object",
					"properties": map[string]any{
						"repo":   map[string]any{"type": "string", "description": "Repository name as recorded in chunk metadata."},
						"path":   map[string]any{"type": "string", "description": "Repo-relative file path as recorded in chunk metadata."},
						"branch": map[string]any{"type": "string", "description": "Optional branch."},
						"start":  map[string]any{"type": "integer", "description": "Optional 1-indexed start line (inclusive)."},
						"end":    map[string]any{"type": "integer", "description": "Optional 1-indexed end line (inclusive)."},
					},
					"required": []string{"repo", "path"},
				},
			},
		},
	}
}

func (k *KnowledgeToolSource) getFileContent(ctx context.Context, argsJSON json.RawMessage) (string, error) {
	if k.resolver == nil {
		return "", errors.New("file content resolver unavailable")
	}
	var args getFileContentArgs
	if len(argsJSON) > 0 {
		if err := json.Unmarshal(argsJSON, &args); err != nil {
			return "", errors.Wrap(err, "unmarshal get_file_content args")
		}
	}
	resp, err := k.resolver.ResolveContent(ctx, index.ContentRequest{
		Repo:   strings.TrimSpace(args.Repo),
		Path:   strings.TrimSpace(args.Path),
		Branch: strings.TrimSpace(args.Branch),
		Start:  args.Start,
		End:    args.End,
	})
	if err != nil {
		return "", errors.Wrap(err, "resolve content")
	}
	b, err := json.Marshal(getFileContentResult{
		Content: resp.Content,
		Source:  resp.Source,
		Found:   resp.Found,
	})
	if err != nil {
		return "", errors.Wrap(err, "marshal get_file_content result")
	}
	return string(b), nil
}

var _ interface {
	Tools(context.Context) ([]openai.ChatCompletionToolUnionParam, error)
	Call(context.Context, string, json.RawMessage) (string, error)
} = (*KnowledgeToolSource)(nil)
