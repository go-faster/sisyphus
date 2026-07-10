package bot

// Markdown -> Telegram formatting-entity rendering.
//
// Telegram has no headings/lists/blockquotes as such; this renderer maps
// Markdown block structure onto plain text with bold/italic/code/link
// entities, so investigation reports (and anything else built from
// Markdown) show up nicely formatted instead of as raw asterisks and hashes.

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gotd/td/telegram/message/entity"
	"github.com/yuin/goldmark"
	gast "github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

var telegramMarkdown = goldmark.New(goldmark.WithExtensions(extension.Table))

// renderMarkdown parses source as Markdown and writes its content into eb
// using Telegram formatting entities.
func renderMarkdown(eb *entity.Builder, source string) error {
	src := []byte(source)
	doc := telegramMarkdown.Parser().Parse(text.NewReader(src))
	r := &mdRenderer{eb: eb, src: src}
	return r.renderChildren(doc, 0)
}

type mdRenderer struct {
	eb  *entity.Builder
	src []byte
}

// renderChildren renders each block-level child of n in sequence.
func (r *mdRenderer) renderChildren(n gast.Node, depth int) error {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if err := r.renderBlock(c, depth); err != nil {
			return err
		}
	}
	return nil
}

func (r *mdRenderer) renderBlock(n gast.Node, depth int) error {
	switch v := n.(type) {
	case *gast.Heading:
		if err := r.renderInline(n, []entity.Formatter{entity.Bold()}); err != nil {
			return err
		}
		r.eb.Plain("\n\n")
	case *gast.Paragraph:
		if err := r.renderInline(n, nil); err != nil {
			return err
		}
		r.eb.Plain("\n\n")
	case *gast.TextBlock:
		if err := r.renderInline(n, nil); err != nil {
			return err
		}
		r.eb.Plain("\n")
	case *gast.List:
		if err := r.renderList(v, depth); err != nil {
			return err
		}
		if depth == 0 {
			r.eb.Plain("\n")
		}
	case *gast.CodeBlock:
		r.eb.Format(codeBlockText(v.Lines(), r.src), entity.Code())
		r.eb.Plain("\n\n")
	case *gast.FencedCodeBlock:
		lang := string(v.Language(r.src))
		r.eb.Format(codeBlockText(v.Lines(), r.src), entity.Pre(lang))
		r.eb.Plain("\n\n")
	case *gast.Blockquote:
		r.eb.Plain("> ")
		if err := r.renderChildren(n, depth); err != nil {
			return err
		}
	case *gast.ThematicBreak:
		// Not representable in Telegram formatting; omit.
	case *east.Table:
		r.renderTable(v)
	default:
		if err := r.renderChildren(n, depth); err != nil {
			return err
		}
	}
	return nil
}

func (r *mdRenderer) renderTable(t *east.Table) {
	rows := r.tableRows(t)
	if len(rows) == 0 {
		return
	}
	widths := tableColumnWidths(rows)
	var sb strings.Builder
	for i, row := range rows {
		if i == 1 {
			writeTableSeparator(&sb, widths)
		}
		for col, width := range widths {
			if col > 0 {
				sb.WriteString(" | ")
			}
			cell := ""
			if col < len(row) {
				cell = row[col]
			}
			sb.WriteString(cell)
			for range width - utf8.RuneCountInString(cell) {
				sb.WriteByte(' ')
			}
		}
		sb.WriteByte('\n')
	}
	r.eb.Format(strings.TrimRight(sb.String(), "\n"), entity.Pre(""))
	r.eb.Plain("\n\n")
}

func (r *mdRenderer) tableRows(t *east.Table) [][]string {
	var rows [][]string
	for rowNode := t.FirstChild(); rowNode != nil; rowNode = rowNode.NextSibling() {
		switch rowNode.(type) {
		case *east.TableHeader, *east.TableRow:
		default:
			continue
		}
		var row []string
		for cellNode := rowNode.FirstChild(); cellNode != nil; cellNode = cellNode.NextSibling() {
			if _, ok := cellNode.(*east.TableCell); !ok {
				continue
			}
			row = append(row, strings.TrimSpace(r.plainText(cellNode)))
		}
		rows = append(rows, row)
	}
	return rows
}

func tableColumnWidths(rows [][]string) []int {
	var widths []int
	for _, row := range rows {
		for col, cell := range row {
			if col >= len(widths) {
				widths = append(widths, 0)
			}
			widths[col] = max(widths[col], utf8.RuneCountInString(cell))
		}
	}
	return widths
}

func writeTableSeparator(sb *strings.Builder, widths []int) {
	for col, width := range widths {
		if col > 0 {
			sb.WriteString("-+-")
		}
		sb.WriteString(strings.Repeat("-", max(width, 3)))
	}
	sb.WriteByte('\n')
}

func (r *mdRenderer) renderList(l *gast.List, depth int) error {
	i := 1
	for c := l.FirstChild(); c != nil; c = c.NextSibling() {
		item, ok := c.(*gast.ListItem)
		if !ok {
			continue
		}
		r.eb.Plain(strings.Repeat("  ", depth))
		if l.IsOrdered() {
			r.eb.Plain(strconv.Itoa(l.Start+i-1) + ". ")
		} else {
			r.eb.Plain("- ")
		}
		if err := r.renderListItemContent(item, depth); err != nil {
			return err
		}
		i++
	}
	return nil
}

// renderListItemContent renders a list item's children inline (rather than
// as full blocks with trailing blank lines), so items stay one line each.
func (r *mdRenderer) renderListItemContent(item *gast.ListItem, depth int) error {
	for c := item.FirstChild(); c != nil; c = c.NextSibling() {
		switch v := c.(type) {
		case *gast.List:
			r.eb.Plain("\n")
			if err := r.renderList(v, depth+1); err != nil {
				return err
			}
		default:
			if err := r.renderInline(c, nil); err != nil {
				return err
			}
			r.eb.Plain("\n")
		}
	}
	return nil
}

// renderInline renders n's inline children, applying an extra set of
// formatters (e.g. Bold for a heading) on top of whatever each leaf already
// carries (emphasis, code, links).
func (r *mdRenderer) renderInline(n gast.Node, extra []entity.Formatter) error {
	return r.walkInline(n, extra)
}

func (r *mdRenderer) walkInline(n gast.Node, active []entity.Formatter) error {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch v := c.(type) {
		case *gast.Text:
			s := string(v.Segment.Value(r.src))
			r.writeLeaf(s, active)
			if v.SoftLineBreak() {
				r.eb.Plain(" ")
			}
			if v.HardLineBreak() {
				r.eb.Plain("\n")
			}
		case *gast.String:
			r.writeLeaf(string(v.Value), active)
		case *gast.CodeSpan:
			r.writeLeaf(r.inlineText(v), append(append([]entity.Formatter{}, active...), entity.Code()))
		case *gast.Emphasis:
			formatter := entity.Italic()
			if v.Level >= 2 {
				formatter = entity.Bold()
			}
			if err := r.walkInline(v, append(append([]entity.Formatter{}, active...), formatter)); err != nil {
				return err
			}
		case *gast.Link:
			if err := r.walkInline(v, append(append([]entity.Formatter{}, active...), entity.TextURL(string(v.Destination)))); err != nil {
				return err
			}
		case *gast.AutoLink:
			r.writeLeaf(string(v.URL(r.src)), append(append([]entity.Formatter{}, active...), entity.URL()))
		case *gast.RawHTML, *gast.Image:
			// Not representable in Telegram formatting; skip.
		default:
			if err := r.walkInline(v, active); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeLeaf appends s with the given formatters, or as plain text if none.
func (r *mdRenderer) writeLeaf(s string, formatters []entity.Formatter) {
	if s == "" {
		return
	}
	if len(formatters) == 0 {
		r.eb.Plain(s)
		return
	}
	r.eb.Format(s, formatters...)
}

// inlineText concatenates a node's text segments (used for CodeSpan, whose
// children are raw Text segments rather than a single value).
func (r *mdRenderer) inlineText(n gast.Node) string {
	var sb strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		t, ok := c.(*gast.Text)
		if !ok {
			continue
		}
		sb.Write(t.Segment.Value(r.src))
	}
	return sb.String()
}

func (r *mdRenderer) plainText(n gast.Node) string {
	var sb strings.Builder
	var walk func(gast.Node)
	walk = func(n gast.Node) {
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			switch v := c.(type) {
			case *gast.Text:
				sb.Write(v.Segment.Value(r.src))
				if v.SoftLineBreak() || v.HardLineBreak() {
					sb.WriteByte(' ')
				}
			case *gast.String:
				sb.Write(v.Value)
			case *gast.CodeSpan:
				sb.WriteString(r.inlineText(v))
			default:
				walk(v)
			}
		}
	}
	walk(n)
	return strings.Join(strings.Fields(sb.String()), " ")
}

func codeBlockText(lines *text.Segments, src []byte) string {
	return strings.TrimSuffix(string(lines.Value(src)), "\n")
}
