package webhook

import (
	"context"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/config"
	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/index"
	"github.com/go-faster/sisyphus/internal/ingestrun"
	"github.com/go-faster/sisyphus/internal/pipeline"
)

// Worker runs provider ingestion triggered by webhooks.
type Worker struct {
	runner ingestrun.Runner
}

// WorkerOptions configures the ingestion worker.
type WorkerOptions struct {
	Logger         *zap.Logger
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
}

func (opts *WorkerOptions) setDefaults() {
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
}

// NewWorker creates a webhook-triggered ingestion worker.
func NewWorker(db *ent.Client, vectors pipeline.VectorStore, embedder index.Embedder, cfg config.Config, opts WorkerOptions) *Worker {
	opts.setDefaults()
	return &Worker{
		runner: ingestrun.Runner{
			DB:       db,
			Vectors:  vectors,
			Embedder: embedder,
			Config:   cfg,
			TP:       opts.TracerProvider,
			MP:       opts.MeterProvider,
		},
	}
}

// RunGitLab runs incremental GitLab ingestion for all enabled resources.
func (w *Worker) RunGitLab(ctx context.Context) error {
	return w.runner.RunGitLab(ctx, ingestrun.GitLabOptions{})
}

// RunJira runs incremental Jira ingestion.
func (w *Worker) RunJira(ctx context.Context) error {
	return w.runner.RunJira(ctx, ingestrun.JiraOptions{})
}
