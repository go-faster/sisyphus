package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-faster/scpbot/internal/index"
)

func TestWalkAll(t *testing.T) {
	t.Run("single source", func(t *testing.T) {
		dir := t.TempDir()
		requireWrite(t, dir, "README.md", "# Hello\nWorld")
		requireWrite(t, dir, "sub", "guide.md", "# Guide\nContent")

		docs, err := WalkAll(context.Background(), []Source{{
			Root:    dir,
			Repo:    "test/repo",
			BaseURL: "https://example.com",
			Branch:  "main",
		}}, WalkOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(docs) != 2 {
			t.Fatalf("want 2 docs, got %d", len(docs))
		}
		if docs[0].Source != index.SourceGitDocs("test/repo") {
			t.Fatalf("unexpected source %q", docs[0].Source)
		}
		if docs[0].SourceID != "test/repo:README.md" {
			t.Fatalf("unexpected SourceID %q", docs[0].SourceID)
		}
		if docs[0].URL != "https://example.com/README.md" {
			t.Fatalf("unexpected URL %q", docs[0].URL)
		}
		if docs[0].Title != "Hello" {
			t.Fatalf("unexpected title %q", docs[0].Title)
		}
		if v, ok := docs[0].Metadata["repo"]; !ok || v != "test/repo" {
			t.Fatalf("missing or wrong repo metadata: %v", docs[0].Metadata)
		}
		if v, ok := docs[0].Metadata["branch"]; !ok || v != "main" {
			t.Fatalf("missing or wrong branch metadata: %v", docs[0].Metadata)
		}
	})

	t.Run("multi source", func(t *testing.T) {
		dir1 := t.TempDir()
		requireWrite(t, dir1, "a.md", "# A")
		dir2 := t.TempDir()
		requireWrite(t, dir2, "b.md", "# B")

		docs, err := WalkAll(context.Background(), []Source{
			{Root: dir1, Repo: "repo/a", Branch: "main"},
			{Root: dir2, Repo: "repo/b", Branch: "main"},
		}, WalkOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(docs) != 2 {
			t.Fatalf("want 2 docs, got %d", len(docs))
		}
		if docs[0].SourceID != "repo/a:a.md" {
			t.Fatalf("unexpected SourceID %q", docs[0].SourceID)
		}
		if docs[1].SourceID != "repo/b:b.md" {
			t.Fatalf("unexpected SourceID %q", docs[1].SourceID)
		}
	})

	t.Run("skip empty root", func(t *testing.T) {
		docs, err := WalkAll(context.Background(), []Source{
			{Root: "", Repo: "empty"},
			{Root: t.TempDir(), Repo: "ok", Branch: "main"},
		}, WalkOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(docs) != 0 {
			t.Fatalf("want 0 docs from empty+empty dir, got %d", len(docs))
		}
	})

	t.Run("Walk fallback", func(t *testing.T) {
		dir := t.TempDir()
		requireWrite(t, dir, "test.md", "# Fallback")

		docs, err := Walk(context.Background(), Source{
			Root: dir, Repo: "fallback", Branch: "main",
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(docs) != 1 {
			t.Fatalf("want 1 doc, got %d", len(docs))
		}
	})

	t.Run("skips vendor and node_modules", func(t *testing.T) {
		dir := t.TempDir()
		requireWrite(t, dir, "doc.md", "# Doc")
		requireWrite(t, dir, "vendor", "dep.md", "# Dep")
		requireWrite(t, dir, "node_modules", "lib.md", "# Lib")

		docs, err := WalkAll(context.Background(), []Source{{
			Root: dir, Repo: "test", Branch: "main",
		}}, WalkOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(docs) != 1 {
			t.Fatalf("want 1 doc (skipped vendor/node_modules), got %d", len(docs))
		}
	})

	t.Run("filters by default exclude and include patterns", func(t *testing.T) {
		dir := t.TempDir()
		requireWrite(t, dir, "README.md", "# README")
		requireWrite(t, dir, "CLAUDE.md", "# CLAUDE")
		requireWrite(t, dir, ".github", "workflows", "ci.md", "# CI")
		requireWrite(t, dir, "LICENSE", "MIT License")
		requireWrite(t, dir, "docs", "guide.md", "# Guide")

		docs, err := WalkAll(context.Background(), []Source{{
			Root: dir, Repo: "test", Branch: "main",
		}}, WalkOptions{})
		if err != nil {
			t.Fatal(err)
		}
		// Only README.md and docs/guide.md should be kept (CLAUDE.md, .github/*, LICENSE filtered out by DefaultExclude)
		if len(docs) != 2 {
			t.Fatalf("want 2 docs (CLAUDE.md, .github/*, LICENSE filtered by DefaultExclude), got %d", len(docs))
		}
		paths := map[string]bool{}
		for _, doc := range docs {
			// Extract path from SourceID (format is "repo:path")
			parts := strings.SplitN(doc.SourceID, ":", 2)
			if len(parts) == 2 {
				paths[parts[1]] = true
			}
		}
		if !paths["README.md"] || !paths["docs/guide.md"] {
			t.Fatalf("unexpected docs kept: %v", paths)
		}
	})

	t.Run("respects Include pattern", func(t *testing.T) {
		dir := t.TempDir()
		requireWrite(t, dir, "README.md", "# README")
		requireWrite(t, dir, "docs", "guide.md", "# Guide")
		requireWrite(t, dir, "blog", "post.md", "# Post")

		docs, err := WalkAll(context.Background(), []Source{{
			Root:    dir,
			Repo:    "test",
			Branch:  "main",
			Include: []string{"docs/**"},
		}}, WalkOptions{})
		if err != nil {
			t.Fatal(err)
		}
		// Only docs/guide.md should be kept (Include restricts to docs/**)
		if len(docs) != 1 {
			t.Fatalf("want 1 doc (only docs/guide.md with Include pattern), got %d", len(docs))
		}
		if !strings.Contains(docs[0].SourceID, "docs/guide.md") {
			t.Fatalf("unexpected doc: %v", docs[0].SourceID)
		}
	})
}

func requireWrite(t *testing.T, parts ...string) {
	t.Helper()
	if len(parts) < 2 {
		t.Fatal("requireWrite needs at least dir + filename")
	}
	dir := parts[0]
	rest := parts[1:]
	if len(rest) < 2 {
		t.Fatal("requireWrite needs content as last arg")
	}
	content := rest[len(rest)-1]
	fileParts := rest[:len(rest)-1]
	fullPath := filepath.Join(append([]string{dir}, fileParts...)...)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
