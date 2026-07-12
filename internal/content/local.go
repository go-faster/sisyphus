// Package content resolves repository files from local clones or the database.
package content

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-faster/errors"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/index"
)

const maxContentBytes = 1 << 20

// RepoResolver maps a repository name to its local filesystem root.
type RepoResolver interface {
	Resolve(repo string) (root string, ok bool)
}

// RepoResolverMap is a simple map-backed RepoResolver.
type RepoResolverMap map[string]string

func (m RepoResolverMap) Resolve(repo string) (string, bool) {
	root, ok := m[repo]
	return root, ok
}

// LocalRepoReader retrieves file content from a local git clone.
type LocalRepoReader struct {
	repos  RepoResolver
	lg     *zap.Logger
	tracer trace.Tracer
}

func NewLocalRepoReader(repos RepoResolver, opts Options) *LocalRepoReader {
	opts.setDefaults()
	return &LocalRepoReader{
		repos:  repos,
		lg:     opts.Logger,
		tracer: opts.TracerProvider.Tracer("github.com/go-faster/sisyphus/internal/content"),
	}
}

func (r *LocalRepoReader) ResolveContent(ctx context.Context, req index.ContentRequest) (_ index.ContentResponse, rerr error) {
	_, span := r.tracer.Start(ctx, "content.LocalRepoReader.ResolveContent",
		trace.WithAttributes(
			attribute.String("repo", req.Repo),
			attribute.String("path", req.Path),
		),
	)
	defer func() {
		if rerr != nil {
			span.RecordError(rerr)
		}
		span.End()
	}()
	root, ok := r.repos.Resolve(req.Repo)
	if !ok {
		return index.ContentResponse{Found: false}, nil
	}

	cleanPath := filepath.Clean(filepath.FromSlash(req.Path))
	if strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) || cleanPath == ".." {
		r.lg.Warn("path traversal attempt rejected", zap.String("path", req.Path))
		return index.ContentResponse{Found: false}, nil
	}

	cleanRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		if os.IsNotExist(err) {
			return index.ContentResponse{Found: false}, nil
		}
		r.lg.Error("failed to resolve repo root", zap.String("root", root), zap.Error(err))
		return index.ContentResponse{Found: false}, nil
	}

	absPath := filepath.Join(cleanRoot, cleanPath)
	resolvedPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return index.ContentResponse{Found: false}, nil
		}
		r.lg.Error("failed to resolve local file", zap.String("path", absPath), zap.Error(err))
		return index.ContentResponse{Found: false}, nil
	}
	if !isPathWithin(cleanRoot, resolvedPath) {
		r.lg.Warn("path resolves outside root", zap.String("path", req.Path), zap.String("root", root))
		return index.ContentResponse{Found: false}, nil
	}

	data, err := readFileLimited(resolvedPath, maxContentBytes)
	if err != nil {
		if os.IsNotExist(err) || err == errContentTooLarge {
			return index.ContentResponse{Found: false}, nil
		}
		r.lg.Error("failed to read local file", zap.String("path", resolvedPath), zap.Error(err))
		return index.ContentResponse{Found: false}, nil
	}

	content := string(data)
	if req.Start > 0 || req.End > 0 {
		lines := strings.Split(content, "\n")
		start := max(req.Start-1, 0)
		end := req.End
		if end <= 0 || end > len(lines) {
			end = len(lines)
		}
		if start >= len(lines) {
			content = ""
		} else {
			if start > end {
				start = end
			}
			content = strings.Join(lines[start:end], "\n")
		}
	}

	return index.ContentResponse{
		Content: content,
		Source:  "local_clone",
		Found:   true,
	}, nil
}

var errContentTooLarge = errors.New("content too large")

func isPathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

func readFileLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // path is resolved and checked against the repo root.
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	if info, err := f.Stat(); err == nil && info.Size() > limit {
		return nil, errContentTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errContentTooLarge
	}
	return data, nil
}
