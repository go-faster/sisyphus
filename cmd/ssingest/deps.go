package main

import (
	"github.com/go-faster/sdk/app"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/go-faster/sisyphus/internal/config"
	"github.com/go-faster/sisyphus/internal/index"
	"github.com/go-faster/sisyphus/internal/pipeline"
	"github.com/go-faster/sisyphus/internal/wire"
)

type ingestDeps struct {
	services  *wire.Services
	cfg       config.Config
	tp        trace.TracerProvider
	mp        metric.MeterProvider
	telemetry *app.Telemetry
}

func newIngestDeps(t *app.Telemetry) *ingestDeps {
	return &ingestDeps{
		tp:        t.TracerProvider(),
		mp:        t.MeterProvider(),
		telemetry: t,
	}
}

func (d *ingestDeps) runner() runner {
	return runner{
		db:       d.services.DB,
		vectors:  d.services.Vectors,
		cfg:      d.cfg,
		tp:       d.tp,
		mp:       d.mp,
		embedder: d.services.Embedder,
	}
}

func (d *ingestDeps) pipeline(ch index.Chunker) (*pipeline.Pipeline, error) {
	return pipeline.New(d.services.DB, ch, d.services.Embedder, d.services.Vectors, pipeline.PipelineOptions{
		TracerProvider: d.tp,
		MeterProvider:  d.mp,
	})
}
