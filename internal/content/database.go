package content

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/ent/document"
	"github.com/go-faster/sisyphus/internal/index"
)

// Options configures DatabaseReader, LocalRepoReader, and ChainResolver construction.
type Options struct {
	Logger         *zap.Logger
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
}

func (o *Options) setDefaults() {
	if o.Logger == nil {
		o.Logger = zap.NewNop()
	}
	if o.TracerProvider == nil {
		o.TracerProvider = otel.GetTracerProvider()
	}
	if o.MeterProvider == nil {
		o.MeterProvider = otel.GetMeterProvider()
	}
}

// DatabaseReader retrieves file content from the Postgres document.body.
type DatabaseReader struct {
	client *ent.Client
	lg     *zap.Logger
	tracer trace.Tracer
}

func NewDatabaseReader(client *ent.Client, opts Options) *DatabaseReader {
	opts.setDefaults()
	return &DatabaseReader{
		client: client,
		lg:     opts.Logger,
		tracer: opts.TracerProvider.Tracer("github.com/go-faster/sisyphus/internal/content"),
	}
}

func (r *DatabaseReader) ResolveContent(ctx context.Context, req index.ContentRequest) (_ index.ContentResponse, rerr error) {
	ctx, span := r.tracer.Start(ctx, "content.DatabaseReader.ResolveContent",
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
		r.lg.Error("failed to query document", zap.Error(err))
		return index.ContentResponse{Found: false}, nil
	}

	content := doc.Body
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
		Source:  "database",
		Found:   true,
	}, nil
}
