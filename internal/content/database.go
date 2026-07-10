package content

import (
	"context"
	"strings"

	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/ent/document"
	"github.com/go-faster/sisyphus/internal/index"
)

// DatabaseReader retrieves file content from the Postgres document.body.
type DatabaseReader struct {
	client *ent.Client
	lg     *zap.Logger
}

func NewDatabaseReader(client *ent.Client, lg *zap.Logger) *DatabaseReader {
	return &DatabaseReader{
		client: client,
		lg:     lg,
	}
}

func (r *DatabaseReader) ResolveContent(ctx context.Context, req index.ContentRequest) (index.ContentResponse, error) {
	// Try the different possible git source prefixes for files.
	prefixes := []string{
		string(index.SourceGitDocs(req.Repo)),
		string(index.SourceGitCode(req.Repo)),
		string(index.SourceGitManifest(req.Repo)),
	}

	sourceID := req.Repo + ":" + req.Path

	var doc *ent.Document
	var err error

	for _, prefix := range prefixes {
		doc, err = r.client.Document.Query().
			Where(
				document.SourceEQ(prefix),
				document.SourceIDEQ(sourceID),
			).
			First(ctx)
		if err == nil && doc != nil {
			break
		}
	}

	if err != nil || doc == nil {
		if ent.IsNotFound(err) || doc == nil {
			return index.ContentResponse{Found: false}, nil
		}
		r.lg.Error("Failed to query document", zap.Error(err))
		return index.ContentResponse{Found: false}, nil
	}

	content := doc.Body
	if req.Start > 0 || req.End > 0 {
		lines := strings.Split(content, "\n")
		start := req.Start - 1
		if start < 0 {
			start = 0
		}
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
		Source:  "database",
		Found:   true,
	}, nil
}
