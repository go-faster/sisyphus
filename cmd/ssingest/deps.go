package main

import (
	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/app"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/go-faster/sisyphus/internal/cliversion"
	"github.com/go-faster/sisyphus/internal/config"
	"github.com/go-faster/sisyphus/internal/indexjob"
	"github.com/go-faster/sisyphus/internal/pipeline"
	"github.com/go-faster/sisyphus/internal/queue"
	"github.com/go-faster/sisyphus/internal/wire"
)

type ingestDeps struct {
	services  *wire.Services
	cfg       config.Config
	tp        trace.TracerProvider
	mp        metric.MeterProvider
	telemetry *app.Telemetry
	info      cliversion.Info
	userAgent string
}

func newIngestDeps(t *app.Telemetry) *ingestDeps {
	info, _ := cliversion.GetInfo("github.com/go-faster/sisyphus")
	return &ingestDeps{
		tp:        t.TracerProvider(),
		mp:        t.MeterProvider(),
		telemetry: t,
		info:      info,
		userAgent: info.UserAgent("ssingest"),
	}
}

// indexerFactory resolves the indexer for a chunker kind.
//
// A run asks for one by kind rather than being handed a pipeline, so the same
// run works unchanged whether the documents are indexed here or handed to a
// worker — that choice lives in whichever factory the runner was built with.
type indexerFactory func(indexjob.Kind) (pipeline.Indexer, error)

// runner builds a runner that indexes in-process. This is what the one-shot
// subcommands use: `ssingest git` must do the whole job by itself, with no
// worker deployed and nothing left on a queue when it exits.
func (d *ingestDeps) runner() runner {
	return d.runnerWith(d.inlineIndexers())
}

func (d *ingestDeps) runnerWith(f indexerFactory) runner {
	return runner{
		db:         d.services.DB,
		vectors:    d.services.Vectors,
		sqlDB:      d.services.SQLDB,
		cfg:        d.cfg,
		tp:         d.tp,
		mp:         d.mp,
		embedder:   d.services.Embedder,
		userAgent:  d.userAgent,
		newIndexer: f,
	}
}

func (d *ingestDeps) inlineIndexers() indexerFactory {
	return func(k indexjob.Kind) (pipeline.Indexer, error) {
		ch, err := indexjob.Chunker(k)
		if err != nil {
			return nil, err
		}
		p, err := pipeline.New(d.services.DB, ch, d.services.Embedder, d.services.Vectors, pipeline.PipelineOptions{
			TracerProvider: d.tp,
			MeterProvider:  d.mp,
		})
		if err != nil {
			return nil, errors.Wrapf(err, "build %s pipeline", k)
		}
		return indexjob.Inline(p), nil
	}
}

// indexQueue is the shared queue index jobs cross.
func (d *ingestDeps) indexQueue() *queue.Postgres {
	w := d.cfg.Ingest.Worker
	return queue.NewPostgres(d.services.DB, indexjob.QueueName, queue.PostgresOptions{
		MaxAttempts: w.MaxAttempts,
		Lease:       w.Lease(),
		Owner:       hostname(),
	})
}

// queueIndexers publishes documents for a worker instead of indexing them here.
func (d *ingestDeps) queueIndexers() indexerFactory {
	q := d.indexQueue()
	return func(k indexjob.Kind) (pipeline.Indexer, error) {
		return indexjob.NewPublisher(q, k, d.services.DB, indexjob.PublisherOptions{
			MaxAttempts: d.cfg.Ingest.Worker.MaxAttempts,
		})
	}
}
