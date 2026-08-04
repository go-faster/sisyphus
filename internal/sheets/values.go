package sheets

import (
	"context"
	"fmt"

	"github.com/go-faster/errors"
	"go.uber.org/zap"
	gsheets "google.golang.org/api/sheets/v4"
)

// Table is a rectangular block of cell values read from a spreadsheet.
type Table struct {
	Spreadsheet string     `json:"spreadsheet"`
	Range       string     `json:"range"`
	Rows        [][]string `json:"rows"`
}

// Update reports what a write touched.
type Update struct {
	Spreadsheet string `json:"spreadsheet"`
	Range       string `json:"range"`
	Rows        int    `json:"rows"`
	Columns     int    `json:"columns"`
	Cells       int    `json:"cells"`
}

// Read returns the values in an A1 range. An empty rng reads the whole first
// tab.
func (c *Client) Read(ctx context.Context, spreadsheet, rng string) (*Table, error) {
	id, err := c.resolve(spreadsheet)
	if err != nil {
		return nil, err
	}
	if rng, err = c.defaultRange(ctx, id, rng); err != nil {
		return nil, err
	}

	resp, err := c.svc.Values.Get(id, rng).Context(ctx).Do()
	if err != nil {
		return nil, errors.Wrap(err, "get values")
	}
	return &Table{
		Spreadsheet: id,
		Range:       resp.Range,
		Rows:        toStrings(resp.Values),
	}, nil
}

// Write overwrites an A1 range with rows. Values are interpreted the way typing
// them in would be, unless raw is set: "1.5" becomes a number and "=SUM(A1:A2)"
// becomes a formula.
func (c *Client) Write(ctx context.Context, spreadsheet, rng string, rows [][]string, raw bool) (*Update, error) {
	if err := c.checkWritable(); err != nil {
		return nil, err
	}
	id, err := c.resolve(spreadsheet)
	if err != nil {
		return nil, err
	}
	if rng == "" {
		return nil, errors.New("write needs an explicit range")
	}

	resp, err := c.svc.Values.
		Update(id, rng, &gsheets.ValueRange{Values: toValues(rows)}).
		ValueInputOption(inputOption(raw)).
		Context(ctx).Do()
	if err != nil {
		return nil, errors.Wrap(err, "update values")
	}
	c.lg.Debug("wrote range",
		zap.String("spreadsheet", id),
		zap.String("range", resp.UpdatedRange),
		zap.Int64("cells", resp.UpdatedCells),
	)
	return &Update{
		Spreadsheet: id,
		Range:       resp.UpdatedRange,
		Rows:        int(resp.UpdatedRows),
		Columns:     int(resp.UpdatedColumns),
		Cells:       int(resp.UpdatedCells),
	}, nil
}

// Append adds rows after the last non-empty row of the table containing rng. An
// empty rng appends to the first tab.
func (c *Client) Append(ctx context.Context, spreadsheet, rng string, rows [][]string, raw bool) (*Update, error) {
	if err := c.checkWritable(); err != nil {
		return nil, err
	}
	id, err := c.resolve(spreadsheet)
	if err != nil {
		return nil, err
	}
	if rng, err = c.defaultRange(ctx, id, rng); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.New("append needs at least one row")
	}

	resp, err := c.svc.Values.
		Append(id, rng, &gsheets.ValueRange{Values: toValues(rows)}).
		ValueInputOption(inputOption(raw)).
		InsertDataOption("INSERT_ROWS").
		Context(ctx).Do()
	if err != nil {
		return nil, errors.Wrap(err, "append values")
	}
	u := &Update{Spreadsheet: id, Range: resp.TableRange}
	if up := resp.Updates; up != nil {
		u.Range = up.UpdatedRange
		u.Rows = int(up.UpdatedRows)
		u.Columns = int(up.UpdatedColumns)
		u.Cells = int(up.UpdatedCells)
	}
	c.lg.Debug("appended rows",
		zap.String("spreadsheet", id),
		zap.String("range", u.Range),
		zap.Int("cells", u.Cells),
	)
	return u, nil
}

// Clear empties the cells in an A1 range, leaving formatting alone.
func (c *Client) Clear(ctx context.Context, spreadsheet, rng string) (*Update, error) {
	if err := c.checkWritable(); err != nil {
		return nil, err
	}
	id, err := c.resolve(spreadsheet)
	if err != nil {
		return nil, err
	}
	if rng == "" {
		return nil, errors.New("clear needs an explicit range")
	}

	resp, err := c.svc.Values.Clear(id, rng, &gsheets.ClearValuesRequest{}).Context(ctx).Do()
	if err != nil {
		return nil, errors.Wrap(err, "clear values")
	}
	c.lg.Debug("cleared range",
		zap.String("spreadsheet", id),
		zap.String("range", resp.ClearedRange),
	)
	return &Update{Spreadsheet: id, Range: resp.ClearedRange}, nil
}

func inputOption(raw bool) string {
	if raw {
		return "RAW"
	}
	return "USER_ENTERED"
}

// toStrings renders API cell values as strings, padding short rows so the
// result is rectangular — the API drops trailing empty cells, which would
// otherwise make row length meaningless to a caller.
func toStrings(values [][]any) [][]string {
	width := 0
	for _, row := range values {
		if len(row) > width {
			width = len(row)
		}
	}
	out := make([][]string, 0, len(values))
	for _, row := range values {
		cells := make([]string, width)
		for i, v := range row {
			if v != nil {
				cells[i] = fmt.Sprint(v)
			}
		}
		out = append(out, cells)
	}
	return out
}

func toValues(rows [][]string) [][]any {
	out := make([][]any, 0, len(rows))
	for _, row := range rows {
		cells := make([]any, len(row))
		for i, v := range row {
			cells[i] = v
		}
		out = append(out, cells)
	}
	return out
}
