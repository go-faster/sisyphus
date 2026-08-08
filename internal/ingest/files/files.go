// Package files ingests configured local files as additional context documents.
package files

import (
	"context"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/go-faster/errors"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/index"
)

// Source describes one named file set.
type Source struct {
	Name      string
	Root      string
	BaseURL   string
	Include   []string
	Exclude   []string
	Authority string
}

// Walk returns one index document per matched file: text files as they are,
// and - when [Options.Converter] is set - office documents converted to
// Markdown.
func Walk(ctx context.Context, sources []Source, opts Options) ([]index.Document, error) {
	opts.setDefaults()

	var docs []index.Document
	for _, src := range sources {
		lg := opts.Logger.With(zap.String("source", src.Name))
		var converted, unconverted int
		if src.Name == "" {
			return nil, errors.New("context file source name is required")
		}
		if src.Root == "" {
			return nil, errors.New("context file source root is required")
		}

		root := filepath.Clean(src.Root)
		err := filepath.WalkDir(root, func(filePath string, d fs.DirEntry, err error) error {
			if err != nil {
				return errors.Wrap(err, "walk context file")
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}

			rel, err := filepath.Rel(root, filePath)
			if err != nil {
				return errors.Wrap(err, "relative context file path")
			}
			rel = filepath.ToSlash(rel)
			if !matches(rel, src.Include, src.Exclude) {
				return nil
			}

			info, err := d.Info()
			if err != nil {
				return errors.Wrap(err, "stat context file")
			}

			ext := strings.ToLower(path.Ext(rel))
			if opts.Converter != nil && opts.Converter.Supports(ext) {
				// Skipped, not fatal: an encrypted spreadsheet must not stop
				// every file walked after it from being indexed.
				body, err := opts.Converter.Convert(ctx, filePath)
				if err != nil {
					unconverted++
					lg.Warn("convert document failed",
						zap.String("path", rel),
						zap.Error(err),
					)
					return nil
				}
				converted++
				docs = append(docs, newDocument(src, rel, body, info.ModTime(), ext))
				return nil
			}

			bodyBytes, err := os.ReadFile(filePath) //nolint:gosec // path from fs walk, not user input
			if err != nil {
				return errors.Wrap(err, "read context file")
			}
			if !utf8.Valid(bodyBytes) {
				return nil
			}

			docs = append(docs, newDocument(src, rel, string(bodyBytes), info.ModTime(), ""))
			return nil
		})
		if err != nil {
			return nil, err
		}
		if converted > 0 || unconverted > 0 {
			lg.Info("converted documents",
				zap.Int("converted", converted),
				zap.Int("unconverted", unconverted),
			)
		}
	}
	return docs, nil
}

func matches(rel string, include, exclude []string) bool {
	if len(include) > 0 {
		matched := false
		for _, pattern := range include {
			if ok, _ := doublestar.Match(pattern, rel); ok {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	for _, pattern := range exclude {
		if ok, _ := doublestar.Match(pattern, rel); ok {
			return false
		}
	}
	return true
}

// newDocument builds the document for one file. convertedFrom is the extension
// the body was converted from, empty when the file was already text.
func newDocument(src Source, rel, body string, modTime time.Time, convertedFrom string) index.Document {
	title := titleFromMarkdown(body)
	if title == "" {
		title = strings.TrimSuffix(path.Base(rel), path.Ext(rel))
	}

	source := index.SourceContextFiles(src.Name)
	url := ""
	if src.BaseURL != "" {
		url = strings.TrimRight(src.BaseURL, "/") + "/" + rel
	}
	authority := src.Authority
	if authority == "" {
		authority = string(index.AuthorityHigh)
	}

	meta := map[string]any{
		"source":    string(source),
		"set":       src.Name,
		"path":      rel,
		"authority": authority,
	}
	if convertedFrom != "" {
		meta["lang"] = "markdown"
		meta["converted_from"] = strings.TrimPrefix(convertedFrom, ".")
	} else if lang := extToLang(filepath.Ext(rel)); lang != "" {
		meta["lang"] = lang
	}
	if url != "" {
		meta["source_url"] = url
	}

	return index.Document{
		ID:        index.NewID(),
		Source:    source,
		SourceID:  src.Name + ":" + rel,
		URL:       url,
		Title:     title,
		Body:      body,
		BodyHash:  index.Hash(body),
		Metadata:  meta,
		UpdatedAt: modTime,
	}
}

func titleFromMarkdown(body string) string {
	for line := range strings.SplitSeq(body, "\n") {
		t := strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(t, "# "); ok {
			return strings.TrimSpace(after)
		}
	}
	return ""
}

func extToLang(ext string) string {
	switch strings.ToLower(ext) {
	case ".md", ".markdown":
		return "markdown"
	case ".txt":
		return "text"
	case ".yaml", ".yml":
		return "yaml"
	case ".json":
		return "json"
	default:
		return strings.TrimPrefix(strings.ToLower(ext), ".")
	}
}
