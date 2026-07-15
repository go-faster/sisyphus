package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/index"
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

	t.Run("mdx doc", func(t *testing.T) {
		dir := t.TempDir()
		requireWrite(t, dir, "guide.mdx", "# Guide\nContent")

		docs, err := WalkAll(context.Background(), []Source{{
			Root:   dir,
			Repo:   "test/repo",
			Branch: "main",
		}}, WalkOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(docs) != 1 {
			t.Fatalf("want 1 doc, got %d", len(docs))
		}
		if docs[0].SourceID != "test/repo:guide.mdx" {
			t.Fatalf("unexpected SourceID %q", docs[0].SourceID)
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

	t.Run("walks manifests when enabled", func(t *testing.T) {
		dir := t.TempDir()
		requireWrite(t, dir, "doc.md", "# Doc")
		requireWrite(t, dir, "deploy.yaml", "kind: Deployment\nmetadata:\n  name: app\n")
		requireWrite(t, dir, "other.yml", "kind: ConfigMap\nmetadata:\n  name: cfg\n")

		docs, err := WalkAll(context.Background(), []Source{{
			Root:      dir,
			Repo:      "test",
			Branch:    "main",
			Manifests: true,
		}}, WalkOptions{})
		require.NoError(t, err)
		require.Len(t, docs, 3)

		var manifestCount int
		for _, d := range docs {
			if d.Source == index.SourceGitManifest("test") {
				manifestCount++
				require.Equal(t, "yaml", d.Metadata["lang"])
			}
		}
		require.Equal(t, 2, manifestCount)
	})

	t.Run("skips manifests when not enabled", func(t *testing.T) {
		dir := t.TempDir()
		requireWrite(t, dir, "doc.md", "# Doc")
		requireWrite(t, dir, "deploy.yaml", "kind: Deployment\n")

		docs, err := WalkAll(context.Background(), []Source{{
			Root:   dir,
			Repo:   "test",
			Branch: "main",
		}}, WalkOptions{})
		require.NoError(t, err)
		require.Len(t, docs, 1)
		require.Equal(t, index.SourceGitDocs("test"), docs[0].Source)
	})

	t.Run("skips large files over byte cap", func(t *testing.T) {
		dir := t.TempDir()
		large := make([]byte, 512*1024)
		requireWrite(t, dir, "big.yaml", string(large))
		requireWrite(t, dir, "small.yaml", "kind: Small\n")

		docs, err := WalkAll(context.Background(), []Source{{
			Root:      dir,
			Repo:      "test",
			Branch:    "main",
			Manifests: true,
		}}, WalkOptions{})
		require.NoError(t, err)
		require.Len(t, docs, 1)
	})

	t.Run("skips generated Go files", func(t *testing.T) {
		dir := t.TempDir()
		requireWrite(t, dir, "main.go", "package main\nfunc main() {}")
		requireWrite(t, dir, "generated.pb.go", "// Code generated by protoc-gen-go. DO NOT EDIT.\npackage pb\n")

		docs, err := WalkAll(context.Background(), []Source{{
			Root: dir,
			Repo: "test",
			Code: true,
		}}, WalkOptions{})
		require.NoError(t, err)
		require.Len(t, docs, 1)
		require.Equal(t, index.SourceGitCode("test"), docs[0].Source)
		require.Contains(t, docs[0].SourceID, "main.go")
	})

	t.Run("applies manifest exclude patterns", func(t *testing.T) {
		dir := t.TempDir()
		requireWrite(t, dir, "good.yaml", "kind: Good\n")
		requireWrite(t, dir, "bad.yaml", "kind: Bad\n")

		docs, err := WalkAll(context.Background(), []Source{{
			Root:            dir,
			Repo:            "test",
			Branch:          "main",
			Manifests:       true,
			ManifestExclude: []string{"bad.yaml"},
		}}, WalkOptions{})
		require.NoError(t, err)
		require.Len(t, docs, 1)
		require.Contains(t, docs[0].SourceID, "good.yaml")
	})

	t.Run("skips new skipDirs", func(t *testing.T) {
		dir := t.TempDir()
		requireWrite(t, dir, "_out", "gen.yaml", "kind: Generated\n")
		requireWrite(t, dir, "_oas", "openapi.yaml", "kind: OpenAPI\n")
		requireWrite(t, dir, "_hack", "script.yaml", "kind: Script\n")
		requireWrite(t, dir, "real.yaml", "kind: Real\n")

		docs, err := WalkAll(context.Background(), []Source{{
			Root:      dir,
			Repo:      "test",
			Branch:    "main",
			Manifests: true,
		}}, WalkOptions{})
		require.NoError(t, err)
		require.Len(t, docs, 1)
		require.Contains(t, docs[0].SourceID, "real.yaml")
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
