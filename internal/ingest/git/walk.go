// Package git ingests git repository content (Markdown docs) and commit
// messages into normalized Documents. It walks a local checkout/working tree
// (cloned/pulled via go-git); the Document output feeds the markdown and git
// commit chunkers and the pipeline.
package git

import (
	"context"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar"
	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"go.uber.org/zap"

	"github.com/go-faster/scpbot/internal/index"
)

// docExtensions are the file types treated as docs.
var docExtensions = map[string]bool{".md": true, ".markdown": true}

// skipDirs are never descended into (plan §3 skip list).
var skipDirs = map[string]bool{
	"node_modules": true, "vendor": true, "dist": true, "build": true,
	"coverage": true, ".git": true,
}

// DefaultExclude are glob patterns (repo-relative slash paths) to skip by default.
var DefaultExclude = []string{
	"CLAUDE.md",
	"**/CLAUDE.md",
	".github/**",
	".gitlab/**",
	"LICENSE",
	"LICENSE.*",
	"**/LICENSE",
	"**/LICENSE.*",
}

// Source describes a docs tree to ingest.
type Source struct {
	// Root is the directory to walk.
	Root string
	// URL is the HTTPS Git remote to clone/fetch before walking.
	URL string
	// Repo is the logical repo name recorded in metadata (e.g. "platform/docs").
	Repo string
	// BaseURL, if set, is prefixed to the relative path to build source_url.
	BaseURL string
	// Branch recorded in metadata.
	Branch string
	// Include is a list of glob patterns (repo-relative) to include. If non-empty,
	// only files matching at least one pattern are kept.
	Include []string
	// Exclude is a list of glob patterns (repo-relative) to skip.
	Exclude []string
	// Commits, if true, also ingest commit messages as Documents.
	Commits bool
}

// WalkOptions configures WalkAll.
type WalkOptions struct{}

// keepFile determines whether a file at the given repo-relative slash path should be kept.
func keepFile(s Source, rel string) bool {
	// Check against DefaultExclude patterns
	for _, pattern := range DefaultExclude {
		if match, _ := doublestar.Match(pattern, rel); match {
			return false
		}
	}
	// Check against Source-specific Exclude patterns
	for _, pattern := range s.Exclude {
		if match, _ := doublestar.Match(pattern, rel); match {
			return false
		}
	}
	// If Include is non-empty, the file must match at least one pattern
	if len(s.Include) > 0 {
		for _, pattern := range s.Include {
			if match, _ := doublestar.Match(pattern, rel); match {
				return true
			}
		}
		return false
	}
	return true
}

// Walk is a convenience wrapper around WalkAll for a single source.
func Walk(ctx context.Context, s Source) ([]index.Document, error) {
	return WalkAll(ctx, []Source{s}, WalkOptions{})
}

// WalkAll walks every source and concatenates their documents.
// Missing or empty roots are logged as a warning but do not fail the walk.
// Documents are returned in source order.
func WalkAll(ctx context.Context, sources []Source, _ WalkOptions) ([]index.Document, error) {
	lg := zctx.From(ctx)

	var docs []index.Document
	for _, s := range sources {
		if s.Root == "" {
			lg.Warn("git: skipping empty root",
				zap.String("repo", s.Repo))
			continue
		}
		err := filepath.WalkDir(s.Root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if skipDirs[d.Name()] {
					return fs.SkipDir
				}
				return nil
			}
			if !docExtensions[strings.ToLower(filepath.Ext(d.Name()))] {
				return nil
			}
			rel, err := filepath.Rel(s.Root, p)
			if err != nil {
				return errors.Wrap(err, "rel path")
			}
			rel = filepath.ToSlash(rel)

			if !keepFile(s, rel) {
				return nil
			}

			body, err := os.ReadFile(p) //nolint:gosec // walking an operator-provided docs root
			if err != nil {
				return errors.Wrap(err, "read file")
			}
			info, err := d.Info()
			if err != nil {
				return errors.Wrap(err, "stat")
			}
			docs = append(docs, newDocument(s, rel, string(body), info.ModTime()))
			return nil
		})
		if err != nil {
			return nil, errors.Wrap(err, "walk docs")
		}
	}
	return docs, nil
}

func newDocument(s Source, rel, body string, mod time.Time) index.Document {
	title := titleFromMarkdown(body)
	if title == "" {
		title = strings.TrimSuffix(path.Base(rel), path.Ext(rel))
	}
	url := ""
	if s.BaseURL != "" {
		url = strings.TrimRight(s.BaseURL, "/") + "/" + rel
	}
	source := index.SourceGitDocs(s.Repo)
	meta := map[string]any{
		"source":    string(source),
		"repo":      s.Repo,
		"path":      rel,
		"branch":    s.Branch,
		"authority": string(index.AuthorityHigh), // docs/runbooks are high authority
	}
	return index.Document{
		ID:        index.NewID(),
		Source:    source,
		SourceID:  s.Repo + ":" + rel,
		URL:       url,
		Title:     title,
		Body:      body,
		BodyHash:  index.Hash(body),
		Metadata:  meta,
		UpdatedAt: mod,
	}
}

// titleFromMarkdown returns the first ATX H1 heading text, if any.
func titleFromMarkdown(body string) string {
	for line := range strings.SplitSeq(body, "\n") {
		t := strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(t, "# "); ok {
			return strings.TrimSpace(after)
		}
	}
	return ""
}
