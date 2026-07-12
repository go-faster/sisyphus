package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/go-faster/sisyphus/internal/index"
)

// AnswerArgs are the input parameters for answer_question.
type AnswerArgs struct {
	Question       string            `json:"question" jsonschema:"The question to answer from the knowledge base."`
	Service        string            `json:"service,omitempty" jsonschema:"Optional service filter for authority boost."`
	Filters        map[string]string `json:"filters,omitempty" jsonschema:"Optional metadata filters. Well-known keys: status, source, jira_project, jira_component, jira_key, authority, repo. Values are always strings."`
	SourceTier     string            `json:"source_tier,omitempty" jsonschema:"Optional source policy: curated (default), code, history, or all. Ignored when filters.source is set."`
	SourcePrefixes []string          `json:"source_prefixes,omitempty" jsonschema:"Optional explicit source prefixes. Overrides source_tier and is ignored when filters.source is set."`
}

// AnswerOut is the structured output for answer_question.
type AnswerOut struct {
	Answer  string         `json:"answer"`
	Results []SearchResult `json:"results"`
}

func answerHandler(retr Retriever, answerer index.Answerer) func(context.Context, *mcp.CallToolRequest, AnswerArgs) (*mcp.CallToolResult, AnswerOut, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args AnswerArgs) (*mcp.CallToolResult, AnswerOut, error) {
		q := index.Query{
			Text:           args.Question,
			Service:        args.Service,
			Filters:        args.Filters,
			SourceTier:     args.SourceTier,
			SourcePrefixes: args.SourcePrefixes,
			Limit:          12,
		}
		results, err := retr.Retrieve(ctx, q)
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "retrieve error: " + err.Error()}},
				IsError: true,
			}, AnswerOut{}, nil
		}

		answer, err := answerer.Answer(ctx, q, results)
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "answer error: " + err.Error()}},
				IsError: true,
			}, AnswerOut{}, nil
		}

		out := AnswerOut{
			Answer:  answer.Text,
			Results: toMCPResults(results),
		}
		return nil, out, nil
	}
}
