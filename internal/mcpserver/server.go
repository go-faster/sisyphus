// Package mcpserver implements an MCP server exposing the knowledge base
// (retrieval + answerer) as MCP tools for Claude Code and vendor CLIs.
package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/go-faster/sisyphus/internal/index"
)

// Retriever is the minimal retrieval interface mcpserver needs.
type Retriever interface {
	Retrieve(ctx context.Context, q index.Query) ([]index.Result, error)
}

// New constructs an MCP Server with knowledge tools wired to the provided
// Retriever and Answerer. Uses official SDK patterns.
func New(retr Retriever, answerer index.Answerer, contentResolver index.ContentResolver) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "ssmcp", Version: "0.1.0"}, nil)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "search_knowledge",
		Description: "Hybrid lexical+vector search over ingested knowledge. Use source_tier=curated by default; opt into code, history, all, or source_prefixes when needed. Returns scored chunks with source URLs.",
	}, searchHandler(retr))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "answer_question",
		Description: "Retrieve relevant context and produce a grounded answer with citations. Use source_tier=code/history/all or source_prefixes when curated sources are not enough.",
	}, answerHandler(retr, answerer))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_file_content",
		Description: "Retrieve actual file content from a source repository by repo + path. Use start/end for line ranges. Uses the local repo clone cache when available, falls back to the stored document body.",
	}, fileHandler(contentResolver))

	return s
}
