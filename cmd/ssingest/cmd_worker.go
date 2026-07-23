package main

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"github.com/go-faster/sisyphus/internal/httpmw"
	"github.com/go-faster/sisyphus/internal/indexjob"
	"github.com/go-faster/sisyphus/internal/mcpserver"
	"github.com/go-faster/sisyphus/internal/pipeline"
	"github.com/go-faster/sisyphus/internal/queue"
)

func hostname() string {
	h, _ := os.Hostname()
	return h
}

func newWorkerCmd(deps *ingestDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "worker",
		Short: "run as an index worker: drain the index queue, chunk, embed and upsert",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runWorker(cmd.Context(), deps)
		},
	}
}

// runWorker runs nothing but the indexing half of ingestion.
//
// It never fetches, so it needs no git clone, no Telegram session and no
// GitLab/Jira credentials — everything it indexes arrives in the job payload.
// That is what makes it safe to scale to N replicas: claims are exclusive for
// a lease, and pipeline.Index is idempotent on (source, source_id), so the
// worst a redelivery costs is repeated embedding work.
func runWorker(ctx context.Context, deps *ingestDeps) error {
	lg := zctx.From(ctx)
	worker, err := newIndexWorker(deps, lg)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mcpserver.InstallHealth(mux, deps.info.Short(), ingestHealthChecker{deps.services})
	srv := &http.Server{
		Addr:              deps.cfg.Ingest.Addr,
		Handler:           httpmw.Wrap(lg, deps.telemetry, mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	lg.Info("starting index worker",
		zap.Int("concurrency", deps.cfg.Ingest.Worker.Concurrency),
		zap.Duration("lease", deps.cfg.Ingest.Worker.Lease()),
	)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return worker.Run(gctx) })
	g.Go(func() error { return httpmw.Serve(gctx, lg, "http", srv) })
	return g.Wait()
}

// newIndexWorker builds the drain loop shared by `ssingest worker` and, unless
// disabled, `ssingest serve`.
func newIndexWorker(deps *ingestDeps, lg *zap.Logger) (*queue.Worker, error) {
	cfg := deps.cfg.Ingest.Worker
	h, err := indexjob.NewHandler(deps.services.DB, deps.services.Embedder, deps.services.Vectors,
		indexjob.HandlerOptions{
			Pipeline: pipeline.PipelineOptions{
				TracerProvider: deps.tp,
				MeterProvider:  deps.mp,
			},
		})
	if err != nil {
		return nil, errors.Wrap(err, "build index handler")
	}
	return queue.NewWorker(deps.indexQueue(), h.Handle, queue.WorkerOptions{
		Concurrency:  cfg.Concurrency,
		PollInterval: cfg.PollInterval(),
		Logger:       lg.Named("index-worker"),
	}), nil
}
