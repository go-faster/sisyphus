package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/go-faster/scpbot/internal/index"
)

// AnswerArgs are the input parameters for answer_question.
type AnswerArgs struct {
	Question string `json:"question" jsonschema:"The question to answer from the knowledge base."`
	Service  string `json:"service,omitempty" jsonschema:"Optional service filter for authority boost."`
}

// AnswerOut is the structured output for answer_question.
type AnswerOut struct {
	Answer  string         `json:"answer"`
	Results []SearchResult `json:"results"`
}

func answerHandler(retr Retriever, answerer index.Answerer) func(context.Context, *mcp.CallToolRequest, AnswerArgs) (*mcp.CallToolResult, AnswerOut, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args AnswerArgs) (*mcp.CallToolResult, AnswerOut, error) {
		q := index.Query{
			Text:    args.Question,
			Service: args.Service,
			Limit:   12,
		}
		results, err := retr.Retrieve(ctx, q)
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "retrieve error: " + err.Error()}},
				IsError: true,
			}, AnswerOut{}, nil
		}

		answer, err := answerer.Answer(ctx, args.Question, results)
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "answer error: " + err.Error()}},
				IsError: true,
			}, AnswerOut{}, nil
		}

		out := AnswerOut{
			Answer:  answer,
			Results: toMCPResults(results),
		}
		return nil, out, nil
	}
}
