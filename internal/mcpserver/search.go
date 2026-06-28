package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/go-faster/scpbot/internal/index"
)

// SearchArgs are the input parameters for search_knowledge.
type SearchArgs struct {
	Query   string `json:"query" jsonschema:"The search query text."`
	Service string `json:"service,omitempty" jsonschema:"Optional service filter for authority boost."`
	Limit   int    `json:"limit,omitempty" jsonschema:"Maximum number of results (default 30)."`
}

// SearchResult mirrors the oas.SearchResult mapping for MCP output.
type SearchResult struct {
	ChunkID    string  `json:"chunk_id"`
	DocumentID string  `json:"document_id"`
	Source     string  `json:"source,omitempty"`
	SourceURL  string  `json:"source_url,omitempty"`
	Title      string  `json:"title,omitempty"`
	ChunkType  string  `json:"chunk_type,omitempty"`
	Text       string  `json:"text"`
	Score      float64 `json:"score"`
	Vector     bool    `json:"vector"`
}

// SearchOut is the structured output for search_knowledge.
type SearchOut struct {
	Results []SearchResult `json:"results"`
}

func searchHandler(retr Retriever) func(context.Context, *mcp.CallToolRequest, SearchArgs) (*mcp.CallToolResult, SearchOut, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args SearchArgs) (*mcp.CallToolResult, SearchOut, error) {
		limit := args.Limit
		if limit <= 0 {
			limit = 30
		}
		q := index.Query{
			Text:    args.Query,
			Service: args.Service,
			Limit:   limit,
		}
		results, err := retr.Retrieve(ctx, q)
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "retrieve error: " + err.Error()}},
				IsError: true,
			}, SearchOut{}, nil
		}

		out := SearchOut{Results: toMCPResults(results)}
		// Return structured; SDK will populate content from it if needed.
		return nil, out, nil
	}
}

func toMCPResults(rs []index.Result) []SearchResult {
	out := make([]SearchResult, 0, len(rs))
	for _, r := range rs {
		sr := SearchResult{
			ChunkID:    r.Chunk.ID.String(),
			DocumentID: r.Chunk.DocumentID.String(),
			Text:       r.Chunk.Text,
			Score:      r.Score,
			Vector:     r.Vector,
		}
		if r.Chunk.Title != "" {
			sr.Title = r.Chunk.Title
		}
		if r.Chunk.Type != "" {
			sr.ChunkType = string(r.Chunk.Type)
		}
		if s := metaString(r.Chunk.Metadata, "source"); s != "" {
			sr.Source = s
		}
		if u := metaString(r.Chunk.Metadata, "source_url"); u != "" {
			sr.SourceURL = u
		}
		out = append(out, sr)
	}
	return out
}

func metaString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
