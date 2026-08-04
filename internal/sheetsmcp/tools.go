package sheetsmcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/go-faster/sisyphus/internal/sheets"
)

type InfoArgs struct {
	Spreadsheet string `json:"spreadsheet,omitempty" jsonschema:"Spreadsheet URL or ID. Omit to use the server's configured default."`
}

type ReadArgs struct {
	Spreadsheet string `json:"spreadsheet,omitempty" jsonschema:"Spreadsheet URL or ID. Omit to use the server's configured default."`
	Range       string `json:"range,omitempty" jsonschema:"A1 range such as 'Sheet1!A1:D20' or 'Sheet1'. Omit to read the whole first tab."`
}

type WriteArgs struct {
	Spreadsheet string     `json:"spreadsheet,omitempty" jsonschema:"Spreadsheet URL or ID. Omit to use the server's configured default."`
	Range       string     `json:"range" jsonschema:"A1 range anchoring the write, such as 'Sheet1!B4'."`
	Rows        [][]string `json:"rows" jsonschema:"Rows of cell values, outer array is rows and inner is columns."`
	Raw         bool       `json:"raw,omitempty" jsonschema:"Store values literally. By default they are parsed as if typed, so '1.5' becomes a number and '=SUM(A1:A2)' a formula."`
}

type AppendArgs struct {
	Spreadsheet string     `json:"spreadsheet,omitempty" jsonschema:"Spreadsheet URL or ID. Omit to use the server's configured default."`
	Range       string     `json:"range,omitempty" jsonschema:"Tab or A1 range identifying the table to append to. Omit to use the first tab."`
	Rows        [][]string `json:"rows" jsonschema:"Rows to append, outer array is rows and inner is columns."`
	Raw         bool       `json:"raw,omitempty" jsonschema:"Store values literally instead of parsing them as if typed."`
}

type ClearArgs struct {
	Spreadsheet string `json:"spreadsheet,omitempty" jsonschema:"Spreadsheet URL or ID. Omit to use the server's configured default."`
	Range       string `json:"range" jsonschema:"A1 range to empty, such as 'Sheet1!A2:D50'."`
}

func infoHandler(s Sheets) func(context.Context, *mcp.CallToolRequest, InfoArgs) (*mcp.CallToolResult, *sheets.Info, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args InfoArgs) (*mcp.CallToolResult, *sheets.Info, error) {
		info, err := s.Info(ctx, args.Spreadsheet)
		if err != nil {
			return toolErr(err), nil, nil
		}
		return nil, info, nil
	}
}

func readHandler(s Sheets) func(context.Context, *mcp.CallToolRequest, ReadArgs) (*mcp.CallToolResult, *sheets.Table, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args ReadArgs) (*mcp.CallToolResult, *sheets.Table, error) {
		table, err := s.Read(ctx, args.Spreadsheet, args.Range)
		if err != nil {
			return toolErr(err), nil, nil
		}
		return nil, table, nil
	}
}

func writeHandler(s Sheets) func(context.Context, *mcp.CallToolRequest, WriteArgs) (*mcp.CallToolResult, *sheets.Update, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args WriteArgs) (*mcp.CallToolResult, *sheets.Update, error) {
		up, err := s.Write(ctx, args.Spreadsheet, args.Range, args.Rows, args.Raw)
		if err != nil {
			return toolErr(err), nil, nil
		}
		return nil, up, nil
	}
}

func appendHandler(s Sheets) func(context.Context, *mcp.CallToolRequest, AppendArgs) (*mcp.CallToolResult, *sheets.Update, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args AppendArgs) (*mcp.CallToolResult, *sheets.Update, error) {
		up, err := s.Append(ctx, args.Spreadsheet, args.Range, args.Rows, args.Raw)
		if err != nil {
			return toolErr(err), nil, nil
		}
		return nil, up, nil
	}
}

func clearHandler(s Sheets) func(context.Context, *mcp.CallToolRequest, ClearArgs) (*mcp.CallToolResult, *sheets.Update, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args ClearArgs) (*mcp.CallToolResult, *sheets.Update, error) {
		up, err := s.Clear(ctx, args.Spreadsheet, args.Range)
		if err != nil {
			return toolErr(err), nil, nil
		}
		return nil, up, nil
	}
}
