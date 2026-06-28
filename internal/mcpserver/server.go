// Package mcpserver implements an MCP server exposing the knowledge base
// (retrieval + answerer) as MCP tools for Claude Code and vendor CLIs.
package mcpserver

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"

	"github.com/go-faster/scpbot/internal/index"
	"github.com/go-faster/scpbot/internal/wire"
)

// Retriever is the retrieval interface (alias to wire.Retriever).
type Retriever = wire.Retriever

// Options holds optional dependencies.
type Options struct {
	Logger *zap.Logger
}

func (o *Options) setDefaults() {
	if o.Logger == nil {
		o.Logger = zap.NewNop()
	}
}

// New constructs an MCP Server with knowledge tools wired to the provided
// Retriever and Answerer. Uses official SDK patterns.
func New(retr Retriever, answerer index.Answerer, opts Options) *mcp.Server {
	opts.setDefaults()
	s := mcp.NewServer(&mcp.Implementation{Name: "scpmcp", Version: "0.1.0"}, nil)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "search_knowledge",
		Description: "Hybrid lexical+vector search over ingested GitLab docs, Jira issues and Telegram support threads. Returns scored chunks with source URLs.",
	}, searchHandler(retr))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "answer_question",
		Description: "Retrieve relevant context and produce a grounded answer with citations.",
	}, answerHandler(retr, answerer))

	return s
}
