package sheetsmcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/go-faster/errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/sheets"
)

// fakeSheets records the last call and returns canned results.
type fakeSheets struct {
	readOnly bool
	err      error

	spreadsheet string
	rng         string
	rows        [][]string
	raw         bool
}

func (f *fakeSheets) Read(_ context.Context, spreadsheet, rng string) (*sheets.Table, error) {
	f.spreadsheet, f.rng = spreadsheet, rng
	if f.err != nil {
		return nil, f.err
	}
	return &sheets.Table{Spreadsheet: "id", Range: "Sheet1!A1:B1", Rows: [][]string{{"a", "b"}}}, nil
}

func (f *fakeSheets) Write(_ context.Context, spreadsheet, rng string, rows [][]string, raw bool) (*sheets.Update, error) {
	f.spreadsheet, f.rng, f.rows, f.raw = spreadsheet, rng, rows, raw
	if f.err != nil {
		return nil, f.err
	}
	return &sheets.Update{Spreadsheet: "id", Range: rng, Cells: 1}, nil
}

func (f *fakeSheets) Append(_ context.Context, spreadsheet, rng string, rows [][]string, raw bool) (*sheets.Update, error) {
	f.spreadsheet, f.rng, f.rows, f.raw = spreadsheet, rng, rows, raw
	if f.err != nil {
		return nil, f.err
	}
	return &sheets.Update{Spreadsheet: "id", Range: "Sheet1!A9", Rows: len(rows)}, nil
}

func (f *fakeSheets) Clear(_ context.Context, spreadsheet, rng string) (*sheets.Update, error) {
	f.spreadsheet, f.rng = spreadsheet, rng
	if f.err != nil {
		return nil, f.err
	}
	return &sheets.Update{Spreadsheet: "id", Range: rng}, nil
}

func (f *fakeSheets) Info(_ context.Context, spreadsheet string) (*sheets.Info, error) {
	f.spreadsheet = spreadsheet
	if f.err != nil {
		return nil, f.err
	}
	return &sheets.Info{Spreadsheet: "id", Title: "Budget", Tabs: []sheets.Tab{{Title: "Sheet1"}}}, nil
}

func (f *fakeSheets) ReadOnly() bool { return f.readOnly }

// connect wires an in-memory client to a server over the SDK's pipe transport.
func connect(t *testing.T, f *fakeSheets) *mcp.ClientSession {
	t.Helper()
	ctx := t.Context()

	clientT, serverT := mcp.NewInMemoryTransports()
	ss, err := New(f, "test").Connect(ctx, serverT, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ss.Close() })

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, clientT, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func toolNames(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	res, err := cs.ListTools(t.Context(), nil)
	require.NoError(t, err)
	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	return names
}

// unmarshalStructured decodes a tool's structured output into v.
func unmarshalStructured(res *mcp.CallToolResult, v any) error {
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func textOf(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

func TestToolsExposed(t *testing.T) {
	t.Run("read-write", func(t *testing.T) {
		require.ElementsMatch(t,
			[]string{"sheets_info", "sheets_read", "sheets_write", "sheets_append", "sheets_clear"},
			toolNames(t, connect(t, &fakeSheets{})),
		)
	})

	// A read-only server must not advertise a tool it would refuse.
	t.Run("read-only hides mutators", func(t *testing.T) {
		require.ElementsMatch(t,
			[]string{"sheets_info", "sheets_read"},
			toolNames(t, connect(t, &fakeSheets{readOnly: true})),
		)
	})
}

func TestReadTool(t *testing.T) {
	f := &fakeSheets{}
	cs := connect(t, f)

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sheets_read",
		Arguments: map[string]any{"range": "Sheet1!A1:B1"},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Equal(t, "Sheet1!A1:B1", f.rng)
	require.Empty(t, f.spreadsheet, "omitted spreadsheet is passed through empty so the client applies its default")

	var out sheets.Table
	require.NoError(t, unmarshalStructured(res, &out))
	require.Equal(t, [][]string{{"a", "b"}}, out.Rows)
}

func TestWriteTool(t *testing.T) {
	f := &fakeSheets{}
	cs := connect(t, f)

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "sheets_write",
		Arguments: map[string]any{
			"spreadsheet": "https://docs.google.com/spreadsheets/d/abc/edit",
			"range":       "B4",
			"rows":        [][]string{{"x", "y"}},
			"raw":         true,
		},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Equal(t, "https://docs.google.com/spreadsheets/d/abc/edit", f.spreadsheet)
	require.Equal(t, "B4", f.rng)
	require.Equal(t, [][]string{{"x", "y"}}, f.rows)
	require.True(t, f.raw)
}

// A client error must reach the model as tool output it can act on, not as a
// protocol error that aborts the call.
func TestErrorIsReportedAsToolResult(t *testing.T) {
	cs := connect(t, &fakeSheets{err: errors.New("range not found")})

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sheets_read",
		Arguments: map[string]any{"range": "Nope!A1"},
	})
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Contains(t, textOf(res), "range not found")
}
