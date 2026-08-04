// Package sheetsmcp exposes a Google Sheets spreadsheet as MCP tools: read a
// range, write a range, append rows, clear a range, list the tabs.
package sheetsmcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/go-faster/sisyphus/internal/sheets"
)

// Sheets is the spreadsheet surface the tools need, satisfied by
// [sheets.Client].
type Sheets interface {
	Read(ctx context.Context, spreadsheet, rng string) (*sheets.Table, error)
	Write(ctx context.Context, spreadsheet, rng string, rows [][]string, raw bool) (*sheets.Update, error)
	Append(ctx context.Context, spreadsheet, rng string, rows [][]string, raw bool) (*sheets.Update, error)
	Clear(ctx context.Context, spreadsheet, rng string) (*sheets.Update, error)
	Info(ctx context.Context, spreadsheet string) (*sheets.Info, error)
	ReadOnly() bool
}

// New builds an MCP server over s. The write tools are omitted entirely when s
// is read-only — a tool the model cannot see is one it cannot plan around.
func New(s Sheets, version string) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "shits", Version: version}, nil)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "sheets_info",
		Description: "List a spreadsheet's tabs with their sizes. Call this first to learn tab names before addressing a range like 'Sheet1!A1:C10'.",
	}, infoHandler(s))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "sheets_read",
		Description: "Read cell values from an A1 range, e.g. 'Sheet1!A1:D20'. Omit range to read the whole first tab. Rows are returned as strings, padded to equal width.",
	}, readHandler(s))

	if s.ReadOnly() {
		return srv
	}

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "sheets_write",
		Description: "Overwrite an A1 range with the given rows. The range anchors the top-left cell; supply as many rows and columns as you want written.",
	}, writeHandler(s))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "sheets_append",
		Description: "Append rows after the last non-empty row of a tab. Use this to add records; use sheets_write to change existing ones.",
	}, appendHandler(s))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "sheets_clear",
		Description: "Empty the cells in an A1 range, leaving formatting intact.",
	}, clearHandler(s))

	return srv
}

// toolErr reports a failure to the model as tool output rather than a protocol
// error, so it can correct a bad range or tab name and retry.
func toolErr(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
		IsError: true,
	}
}
