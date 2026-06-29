package gitlab

import (
	"context"
	"os"
	"path/filepath"
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
		if docs[0].Source != index.SourceGitLabDocs {
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
