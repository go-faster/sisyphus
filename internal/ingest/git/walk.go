// Package git ingests git repository content (Markdown docs) and commit
// messages into normalized Documents. It walks a local checkout/working tree
// (cloned/pulled via go-git); the Document output feeds the markdown and git
// commit chunkers and the pipeline.
package git

import (
	"bufio"
	"context"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/index"
)

// fileKind classifies a file during walk.
type fileKind int

const (
	kindDoc fileKind = iota
	kindManifest
	kindCode
)

// docExtensions are the file types treated as docs.
var docExtensions = map[string]bool{".md": true, ".markdown": true}

// manifestExtensions are YAML file types.
var manifestExtensions = map[string]bool{".yaml": true, ".yml": true}

// codeExtensions are source code file types.
var codeExtensions = map[string]bool{
	".go": true, ".ts": true, ".tsx": true, ".proto": true, ".sql": true,
}

// skipDirs are never descended into.
var skipDirs = map[string]bool{
	"node_modules": true, "vendor": true, "dist": true, "build": true,
	"coverage": true, ".git": true,
	"_out": true, "_oas": true, "_hack": true,
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
	// Generated code bundles
	"**/*.pb.go",
	"**/*_gen.go",
	"**/*.gen.go",
	"**/zz_generated*.go",
	"**/mock_*.go",
	"**/*.gen.ts",
	"**/routeTree.gen.ts",
	// Known vendored/generated YAML bundles
	"**/kafka-op.yml",
	"**/cnpg.yaml",
	"**/gotk-components.yaml",
	"**/crd/**",
	"**/*dashboards*.yml",
}

// MaxFileBytes is the maximum file body size to index (0 = default 256 KB).
var MaxFileBytes int64

func getMaxFileBytes() int64 {
	if MaxFileBytes > 0 {
		return MaxFileBytes
	}
	return 256 * 1024
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
	// Tags, if true, also ingest git tags as Documents.
	Tags bool
	// Manifests, if true, also walk YAML manifests.
	Manifests bool
	// Code, if true, also walk source code files (Go/TS/proto/SQL).
	Code bool
	// ManifestExclude are additional excludes applied only when walking manifests.
	ManifestExclude []string
	// CodeInclude restricts code-walk to paths matching these globs.
	CodeInclude []string
	// CodeExclude skips code files matching these globs.
	CodeExclude []string
}

// WalkOptions configures WalkAll.
type WalkOptions struct{}

// classifyFile determines the file kind based on extension and enabled source toggles.
func classifyFile(rel string, s Source) fileKind {
	ext := strings.ToLower(filepath.Ext(rel))
	if docExtensions[ext] {
		return kindDoc
	}
	if s.Manifests && manifestExtensions[ext] {
		return kindManifest
	}
	if s.Code && codeExtensions[ext] {
		return kindCode
	}
	return -1 // unknown / not enabled
}

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

// keepCodeFile applies code-specific include/exclude globs.
func keepCodeFile(s Source, rel string) bool {
	for _, pattern := range s.CodeExclude {
		if match, _ := doublestar.Match(pattern, rel); match {
			return false
		}
	}
	if len(s.CodeInclude) > 0 {
		for _, pattern := range s.CodeInclude {
			if match, _ := doublestar.Match(pattern, rel); match {
				return true
			}
		}
		return false
	}
	return true
}

// isGeneratedGo checks whether body begins with the standard Go generated header.
func isGeneratedGo(body string) bool {
	scanner := bufio.NewScanner(strings.NewReader(body))
	scanner.Scan()
	line := scanner.Text()
	if strings.Contains(line, "Code generated") && strings.Contains(line, "DO NOT EDIT") {
		return true
	}
	return false
}

// Walk is a convenience wrapper around WalkAll for a single source.
func Walk(ctx context.Context, s Source) ([]index.Document, error) {
	return WalkAll(ctx, []Source{s}, WalkOptions{})
}

// WalkAll walks every source and concatenates their documents. It classifies
// files by extension and produces Documents with the appropriate source prefix
// (git_docs:, git_manifest:, or git_code:).
// Missing or empty roots are logged as a warning but do not fail the walk.
// Documents are returned in source order.
func WalkAll(ctx context.Context, sources []Source, _ WalkOptions) ([]index.Document, error) {
	lg := zctx.From(ctx)
	maxBytes := getMaxFileBytes()

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
			rel, err := filepath.Rel(s.Root, p)
			if err != nil {
				return errors.Wrap(err, "rel path")
			}
			rel = filepath.ToSlash(rel)

			kind := classifyFile(rel, s)
			if kind < 0 {
				return nil
			}

			if !keepFile(s, rel) {
				return nil
			}

			// Kind-specific exclude/include
			switch kind {
			case kindManifest:
				for _, pattern := range s.ManifestExclude {
					if match, _ := doublestar.Match(pattern, rel); match {
						return nil
					}
				}
			case kindCode:
				if !keepCodeFile(s, rel) {
					return nil
				}
			}

			// Check file size before reading
			info, err := d.Info()
			if err != nil {
				return errors.Wrap(err, "stat")
			}
			if info.Size() > maxBytes {
				lg.Debug("skipping file over byte cap",
					zap.String("path", rel),
					zap.Int64("size", info.Size()),
					zap.Int64("max", maxBytes))
				return nil
			}

			body, err := os.ReadFile(p) //nolint:gosec // walking an operator-provided docs root
			if err != nil {
				return errors.Wrap(err, "read file")
			}

			// Generated-header sniff for code files
			if kind == kindCode && isGeneratedGo(string(body)) {
				lg.Debug("skipping generated Go file", zap.String("path", rel))
				return nil
			}

			docs = append(docs, newDocumentForKind(s, rel, string(body), info.ModTime(), kind))
			return nil
		})
		if err != nil {
			return nil, errors.Wrap(err, "walk docs")
		}
	}
	return docs, nil
}

func newDocumentForKind(s Source, rel, body string, mod time.Time, kind fileKind) index.Document {
	switch kind {
	case kindManifest:
		return newManifestDocument(s, rel, body, mod)
	case kindCode:
		return newCodeDocument(s, rel, body, mod)
	default:
		return newDocDocument(s, rel, body, mod)
	}
}

func newDocDocument(s Source, rel, body string, mod time.Time) index.Document {
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
		"authority": string(index.AuthorityHigh),
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

func newManifestDocument(s Source, rel, body string, mod time.Time) index.Document {
	title := strings.TrimSuffix(path.Base(rel), path.Ext(rel))
	url := ""
	if s.BaseURL != "" {
		url = strings.TrimRight(s.BaseURL, "/") + "/" + rel
	}
	source := index.SourceGitManifest(s.Repo)
	meta := map[string]any{
		"source":    string(source),
		"repo":      s.Repo,
		"path":      rel,
		"branch":    s.Branch,
		"lang":      "yaml",
		"authority": string(index.AuthorityMedium),
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

func newCodeDocument(s Source, rel, body string, mod time.Time) index.Document {
	ext := strings.ToLower(filepath.Ext(rel))
	lang := extToLang(ext)
	title := strings.TrimSuffix(path.Base(rel), ext)
	url := ""
	if s.BaseURL != "" {
		url = strings.TrimRight(s.BaseURL, "/") + "/" + rel
	}
	source := index.SourceGitCode(s.Repo)
	meta := map[string]any{
		"source":    string(source),
		"repo":      s.Repo,
		"path":      rel,
		"branch":    s.Branch,
		"lang":      lang,
		"authority": string(index.AuthorityLowMedium),
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

func extToLang(ext string) string {
	switch ext {
	case ".go":
		return "go"
	case ".ts", ".tsx":
		return "typescript"
	case ".proto":
		return "proto"
	case ".sql":
		return "sql"
	default:
		return strings.TrimPrefix(ext, ".")
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
