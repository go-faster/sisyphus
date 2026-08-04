package sheets

import (
	"context"

	"github.com/go-faster/errors"
)

// Info describes a spreadsheet and its tabs.
type Info struct {
	Spreadsheet string `json:"spreadsheet"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Tabs        []Tab  `json:"tabs"`
}

// Tab is one sheet within a spreadsheet.
type Tab struct {
	Title   string `json:"title"`
	ID      int64  `json:"id"`
	Index   int64  `json:"index"`
	Rows    int64  `json:"rows"`
	Columns int64  `json:"columns"`
}

// Info returns the spreadsheet title and its tabs. Call it to discover tab
// names before addressing a range like 'Sheet1!A1:C10'.
func (c *Client) Info(ctx context.Context, spreadsheet string) (*Info, error) {
	id, err := c.resolve(spreadsheet)
	if err != nil {
		return nil, err
	}

	resp, err := c.svc.Get(id).
		Fields("properties.title,spreadsheetUrl,sheets.properties").
		Context(ctx).Do()
	if err != nil {
		return nil, errors.Wrap(err, "get spreadsheet")
	}

	info := &Info{Spreadsheet: id, URL: resp.SpreadsheetUrl}
	if p := resp.Properties; p != nil {
		info.Title = p.Title
	}
	for _, sh := range resp.Sheets {
		p := sh.Properties
		if p == nil {
			continue
		}
		tab := Tab{Title: p.Title, ID: p.SheetId, Index: p.Index}
		if g := p.GridProperties; g != nil {
			tab.Rows, tab.Columns = g.RowCount, g.ColumnCount
		}
		info.Tabs = append(info.Tabs, tab)
	}
	return info, nil
}

// defaultRange fills in an omitted range with the first tab's title, which the
// API reads as "the whole tab". Values.Get and Values.Append both require one.
func (c *Client) defaultRange(ctx context.Context, id, rng string) (string, error) {
	if rng != "" {
		return rng, nil
	}
	info, err := c.Info(ctx, id)
	if err != nil {
		return "", errors.Wrap(err, "resolve default range")
	}
	if len(info.Tabs) == 0 {
		return "", errors.Errorf("spreadsheet %s has no tabs", id)
	}
	return info.Tabs[0].Title, nil
}
