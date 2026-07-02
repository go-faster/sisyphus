package pipeline

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type pipelineMetrics struct {
	documents metric.Int64Counter
	chunks    metric.Int64Counter
	embedDur  metric.Float64Histogram
	phaseDur  metric.Float64Histogram
	lockWait  metric.Float64Histogram
}

func newPipelineMetrics(mp metric.MeterProvider) (*pipelineMetrics, error) {
	meter := mp.Meter("github.com/go-faster/sisyphus/pipeline")

	documents, err := meter.Int64Counter(
		"sisyphus.pipeline.documents",
		metric.WithDescription("Count of processed documents by source and status"),
	)
	if err != nil {
		return nil, err
	}

	chunks, err := meter.Int64Counter(
		"sisyphus.pipeline.chunks",
		metric.WithDescription("Count of chunks by embedding status"),
	)
	if err != nil {
		return nil, err
	}

	embedDur, err := meter.Float64Histogram(
		"sisyphus.pipeline.embed.duration",
		metric.WithDescription("Duration of embedding operation"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}
	phaseDur, err := meter.Float64Histogram(
		"sisyphus.pipeline.phase.duration",
		metric.WithDescription("Duration of pipeline phases"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}
	lockWait, err := meter.Float64Histogram(
		"sisyphus.pipeline.lock_wait.duration",
		metric.WithDescription("Duration waiting for per-document pipeline lock"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}

	return &pipelineMetrics{
		documents: documents,
		chunks:    chunks,
		embedDur:  embedDur,
		phaseDur:  phaseDur,
		lockWait:  lockWait,
	}, nil
}

func (m *pipelineMetrics) recordPhase(ctx context.Context, source, phase string, durSeconds float64, err error) {
	status := "ok"
	if err != nil {
		status = "error"
	}
	m.phaseDur.Record(ctx, durSeconds,
		metric.WithAttributes(
			attribute.String("source", source),
			attribute.String("phase", phase),
			attribute.String("status", status),
		),
	)
}
