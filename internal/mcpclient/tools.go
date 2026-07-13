package mcpclient

import (
	"context"

	"github.com/go-faster/errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/shared"
)

// Tools retrieves all available tools from the connected MCP gateway
// and maps them to OpenAI tool definitions.
func (c *Client) Tools(ctx context.Context) ([]openai.ChatCompletionToolUnionParam, error) {
	var allTools []openai.ChatCompletionToolUnionParam
	var cursor string

	for {
		var req mcp.ListToolsParams
		if cursor != "" {
			req.Cursor = cursor
		}

		res, err := withSession(ctx, c, func(session *mcp.ClientSession) (*mcp.ListToolsResult, error) {
			return session.ListTools(ctx, &req)
		})
		if err != nil {
			return nil, errors.Wrap(err, "list tools")
		}

		for _, t := range res.Tools {
			schema := shared.FunctionParameters{
				"type":       "object",
				"properties": map[string]any{},
			}
			if m, ok := t.InputSchema.(map[string]any); ok && len(m) > 0 {
				schema = shared.FunctionParameters(m)
			}
			desc := param.Opt[string]{}
			if t.Description != "" {
				desc = param.NewOpt(t.Description)
			}

			// Map tool schema into the ChatCompletionToolUnionParam format.
			allTools = append(allTools, openai.ChatCompletionToolUnionParam{
				OfFunction: &openai.ChatCompletionFunctionToolParam{
					Function: openai.FunctionDefinitionParam{
						Name:        t.Name,
						Description: desc,
						Parameters:  schema,
					},
				},
			})
		}

		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}

	return allTools, nil
}
