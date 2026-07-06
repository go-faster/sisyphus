// Package code implements a source code chunker. Go files use AST symbol-level
// chunking via go/ast; all other languages use a generic blank-line-delimited
// block splitter.
package code

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"path/filepath"
	"strings"

	"github.com/go-faster/sisyphus/internal/index"
)

const (
	defaultMaxWindowRunes = 4000
	defaultOverlapRunes   = 300
)

// Chunker splits source code documents into chunks. Go files use AST parsing
// for symbol-level chunks; all other languages use a generic line-window splitter.
type Chunker struct {
	maxWindowRunes int
	overlapRunes   int
}

// ChunkerOptions configures a Chunker.
type ChunkerOptions struct {
	// MaxWindowRunes is the maximum rune budget for a window before splitting.
	// Default is ~4000 runes.
	MaxWindowRunes int
	// OverlapRunes is the overlap runes when splitting long windows.
	// Default is ~300 runes.
	OverlapRunes int
}

func (opts *ChunkerOptions) setDefaults() {
	if opts.MaxWindowRunes == 0 {
		opts.MaxWindowRunes = defaultMaxWindowRunes
	}
	if opts.OverlapRunes == 0 {
		opts.OverlapRunes = defaultOverlapRunes
	}
}

// New creates a new code chunker.
func New(opts ChunkerOptions) *Chunker {
	opts.setDefaults()
	return &Chunker{
		maxWindowRunes: opts.MaxWindowRunes,
		overlapRunes:   opts.OverlapRunes,
	}
}

// Chunk implements index.Chunker.
func (c *Chunker) Chunk(_ context.Context, doc index.Document) ([]index.Chunk, error) {
	if doc.Body == "" {
		return nil, nil
	}

	lang, _ := doc.Metadata["lang"].(string)

	switch lang {
	case "go":
		return c.chunkGo(doc)
	default:
		return c.chunkGeneric(doc)
	}
}

// chunkGo uses go/ast to split a Go file into symbol-level chunks.
func (c *Chunker) chunkGo(doc index.Document) ([]index.Chunk, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", doc.Body, parser.ParseComments)
	if err != nil {
		return nil, nil
	}

	var chunks []index.Chunk

	// File overview chunk with package, imports, and symbol signatures
	overview := c.buildOverview(doc, f)
	if overview != nil {
		chunks = append(chunks, *overview)
	}

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			if d.Tok == token.IMPORT {
				continue
			}
			for _, spec := range d.Specs {
				if c := c.genDeclChunk(doc, d, spec, fset); c != nil {
					chunks = append(chunks, *c)
				}
			}
		case *ast.FuncDecl:
			if c := c.funcDeclChunk(doc, d, fset); c != nil {
				chunks = append(chunks, *c)
			}
		}
	}
	for i := range chunks {
		chunks[i].Index = i
	}

	return chunks, nil
}

func (c *Chunker) buildOverview(doc index.Document, f *ast.File) *index.Chunk {
	pkgName := ""
	if f.Name != nil {
		pkgName = f.Name.Name
	}

	var sb strings.Builder
	sb.WriteString("package " + pkgName + "\n\n")

	if len(f.Imports) > 0 {
		sb.WriteString("import (\n")
		for _, imp := range f.Imports {
			path := ""
			if imp.Path != nil {
				path = imp.Path.Value
			}
			sb.WriteString("\t" + path + "\n")
		}
		sb.WriteString(")\n\n")
	}

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			sig := c.funcSig(d)
			if sig != "" {
				sb.WriteString(sig + "\n")
			}
		case *ast.GenDecl:
			if d.Tok == token.IMPORT {
				continue
			}
			for _, spec := range d.Specs {
				if s := c.specSig(spec); s != "" {
					sb.WriteString(s + "\n")
				}
			}
		}
	}

	text := strings.TrimSpace(sb.String())
	if text == "" {
		return nil
	}

	meta := make(map[string]any)
	maps.Copy(meta, doc.Metadata)
	meta["symbol"] = pkgName
	meta["symbol_kind"] = "package"

	return &index.Chunk{
		ID:         index.NewID(),
		DocumentID: doc.ID,
		Index:      0,
		Type:       index.ChunkCodeFile,
		Title:      fmt.Sprintf("file: %s (%s)", doc.Title, pkgName),
		Text:       text,
		TextHash:   index.Hash(text),
		Metadata:   meta,
	}
}

func (c *Chunker) funcDeclChunk(doc index.Document, d *ast.FuncDecl, fset *token.FileSet) *index.Chunk {
	startPos := d.Pos()
	if d.Doc != nil {
		startPos = d.Doc.Pos()
	}
	start := fset.Position(startPos)
	end := fset.Position(d.End())

	body := extractSource(doc.Body, start.Offset, end.Offset)

	meta := make(map[string]any)
	maps.Copy(meta, doc.Metadata)
	meta["symbol"] = d.Name.Name
	meta["symbol_kind"] = "func"
	if d.Recv != nil && len(d.Recv.List) > 0 {
		recvType := exprString(d.Recv.List[0].Type)
		meta["receiver"] = recvType
	}

	title := d.Name.Name
	if recv, _ := meta["receiver"].(string); recv != "" {
		title = fmt.Sprintf("(%s).%s", recv, d.Name.Name)
	}

	return &index.Chunk{
		ID:         index.NewID(),
		DocumentID: doc.ID,
		Index:      0,
		Type:       index.ChunkCodeSymbol,
		Title:      title,
		Text:       body,
		TextHash:   index.Hash(body),
		Metadata:   meta,
	}
}

func (c *Chunker) genDeclChunk(doc index.Document, d *ast.GenDecl, spec ast.Spec, fset *token.FileSet) *index.Chunk {
	startPos := specStart(d, spec)
	start := fset.Position(startPos)
	end := fset.Position(spec.End())

	body := extractSource(doc.Body, start.Offset, end.Offset)

	meta := make(map[string]any)
	maps.Copy(meta, doc.Metadata)

	var symbol, kind string
	switch s := spec.(type) {
	case *ast.TypeSpec:
		symbol = s.Name.Name
		kind = "type"
	case *ast.ValueSpec:
		if len(s.Names) > 0 {
			symbol = s.Names[0].Name
		}
		kind = strings.ToLower(d.Tok.String())
	case *ast.ImportSpec:
		return nil
	}

	meta["symbol"] = symbol
	meta["symbol_kind"] = kind

	return &index.Chunk{
		ID:         index.NewID(),
		DocumentID: doc.ID,
		Index:      0,
		Type:       index.ChunkCodeSymbol,
		Title:      symbol,
		Text:       body,
		TextHash:   index.Hash(body),
		Metadata:   meta,
	}
}

// specStart returns the start position of a spec including its doc comment. For a
// grouped decl the comment lives on the spec; for a single (unparenthesized) decl
// like "// Foo does X\ntype Foo struct{}" it lives on the GenDecl.
func specStart(d *ast.GenDecl, spec ast.Spec) token.Pos {
	if !d.Lparen.IsValid() && d.Doc != nil {
		return d.Doc.Pos()
	}
	switch s := spec.(type) {
	case *ast.TypeSpec:
		if s.Doc != nil {
			return s.Doc.Pos()
		}
	case *ast.ValueSpec:
		if s.Doc != nil {
			return s.Doc.Pos()
		}
	}
	return spec.Pos()
}

func (c *Chunker) funcSig(d *ast.FuncDecl) string {
	var sb strings.Builder
	if d.Recv != nil && len(d.Recv.List) > 0 {
		sb.WriteString("func (" + exprString(d.Recv.List[0].Type) + ") ")
	} else {
		sb.WriteString("func ")
	}
	sb.WriteString(d.Name.Name)
	sb.WriteString("(")
	if d.Type.Params != nil {
		for i, p := range d.Type.Params.List {
			if i > 0 {
				sb.WriteString(", ")
			}
			for j, n := range p.Names {
				if j > 0 {
					sb.WriteString(", ")
				}
				sb.WriteString(n.Name)
			}
			sb.WriteString(" " + exprString(p.Type))
		}
	}
	sb.WriteString(")")
	if d.Type.Results != nil && len(d.Type.Results.List) > 0 {
		sb.WriteString(" ")
		if len(d.Type.Results.List) == 1 && len(d.Type.Results.List[0].Names) == 0 {
			sb.WriteString(exprString(d.Type.Results.List[0].Type))
		} else {
			sb.WriteString("(")
			for i, r := range d.Type.Results.List {
				if i > 0 {
					sb.WriteString(", ")
				}
				for j, n := range r.Names {
					if j > 0 {
						sb.WriteString(", ")
					}
					sb.WriteString(n.Name)
				}
				sb.WriteString(" " + exprString(r.Type))
			}
			sb.WriteString(")")
		}
	}
	return sb.String()
}

func (c *Chunker) specSig(spec ast.Spec) string {
	switch s := spec.(type) {
	case *ast.TypeSpec:
		return "type " + s.Name.Name + " " + exprString(s.Type)
	case *ast.ValueSpec:
		if len(s.Names) == 0 {
			return ""
		}
		return s.Names[0].Name + " " + exprString(s.Type)
	}
	return ""
}

// chunkGeneric implements a line-window splitter for non-Go files.
func (c *Chunker) chunkGeneric(doc index.Document) ([]index.Chunk, error) {
	lines := strings.Split(doc.Body, "\n")

	var chunks []index.Chunk
	chunkIdx := 0

	// Group into blocks separated by blank lines
	var blocks [][]string
	var current []string

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			if len(current) > 0 {
				blocks = append(blocks, current)
				current = nil
			}
			continue
		}
		current = append(current, line)
	}
	if len(current) > 0 {
		blocks = append(blocks, current)
	}

	if len(blocks) == 0 {
		return nil, nil
	}

	for _, block := range blocks {
		for _, blockText := range c.splitWindow(strings.Join(block, "\n")) {
			if blockText == "" {
				continue
			}

			title := extractTitle(strings.Split(blockText, "\n"))

			meta := make(map[string]any)
			maps.Copy(meta, doc.Metadata)
			if ext := filepath.Ext(doc.Title); ext != "" {
				meta["lang"] = strings.TrimPrefix(ext, ".")
			}

			chunks = append(chunks, index.Chunk{
				ID:         index.NewID(),
				DocumentID: doc.ID,
				Index:      chunkIdx,
				Type:       index.ChunkCodeSymbol,
				Title:      title,
				Text:       blockText,
				TextHash:   index.Hash(blockText),
				Metadata:   meta,
			})
			chunkIdx++
		}
	}

	return chunks, nil
}

func (c *Chunker) splitWindow(text string) []string {
	runes := []rune(text)
	if len(runes) <= c.maxWindowRunes {
		return []string{text}
	}

	overlap := min(c.overlapRunes, c.maxWindowRunes/2)
	step := c.maxWindowRunes - overlap
	if step <= 0 {
		step = c.maxWindowRunes
	}

	chunks := make([]string, 0, (len(runes)+step-1)/step)
	for start := 0; start < len(runes); start += step {
		end := min(start+c.maxWindowRunes, len(runes))
		chunks = append(chunks, string(runes[start:end]))
		if end == len(runes) {
			break
		}
	}
	return chunks
}

func extractTitle(block []string) string {
	if len(block) == 0 {
		return ""
	}
	first := strings.TrimSpace(block[0])
	if first == "" && len(block) > 1 {
		first = strings.TrimSpace(block[1])
	}
	if len(first) > 60 {
		first = first[:60] + "..."
	}
	return first
}

func exprString(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + exprString(t.X)
	case *ast.SelectorExpr:
		return exprString(t.X) + "." + t.Sel.Name
	case *ast.ArrayType:
		if t.Len == nil {
			return "[]" + exprString(t.Elt)
		}
		return "[" + exprString(t.Len) + "]" + exprString(t.Elt)
	case *ast.MapType:
		return "map[" + exprString(t.Key) + "]" + exprString(t.Value)
	case *ast.InterfaceType:
		return "interface{}"
	case *ast.StructType:
		return "struct{...}"
	case *ast.Ellipsis:
		return "..." + exprString(t.Elt)
	case *ast.BasicLit:
		return t.Value
	default:
		return fmt.Sprintf("%T", e)
	}
}

func extractSource(body string, start, end int) string {
	if start < 0 {
		start = 0
	}
	if end > len(body) {
		end = len(body)
	}
	if start >= end {
		return ""
	}
	return body[start:end]
}
