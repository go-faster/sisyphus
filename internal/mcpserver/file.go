package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/go-faster/sisyphus/internal/index"
)

type FileArgs struct {
	Repo   string `json:"repo" jsonschema:"Repository name as recorded in chunk metadata"`
	Path   string `json:"path" jsonschema:"Repo-relative file path"`
	Branch string `json:"branch,omitempty" jsonschema:"Optional branch"`
	Start  int    `json:"start,omitempty" jsonschema:"Optional 1-indexed start line (inclusive)"`
	End    int    `json:"end,omitempty" jsonschema:"Optional 1-indexed end line (inclusive)"`
}

type FileOut struct {
	Content string `json:"content"`
	Source  string `json:"source"`
	Found   bool   `json:"found"`
}

func fileHandler(r index.ContentResolver) func(context.Context, *mcp.CallToolRequest, FileArgs) (*mcp.CallToolResult, FileOut, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args FileArgs) (*mcp.CallToolResult, FileOut, error) {
		if r == nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "file content resolver not configured"}},
				IsError: true,
			}, FileOut{}, nil
		}

		req := index.ContentRequest{
			Repo:   args.Repo,
			Path:   args.Path,
			Branch: args.Branch,
			Start:  args.Start,
			End:    args.End,
		}

		resp, err := r.ResolveContent(ctx, req)
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "resolve content: " + err.Error()}},
				IsError: true,
			}, FileOut{}, nil
		}

		if !resp.Found {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "file not found"}},
				IsError: true,
			}, FileOut{}, nil
		}

		out := FileOut{
			Content: resp.Content,
			Source:  resp.Source,
			Found:   resp.Found,
		}

		return nil, out, nil
	}
}
