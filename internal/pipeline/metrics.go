package pipeline

import (
	"go.opentelemetry.io/otel/metric"
)

type pipelineMetrics struct {
	documents metric.Int64Counter
	chunks    metric.Int64Counter
	embedDur  metric.Float64Histogram
}

func newPipelineMetrics(mp metric.MeterProvider) (*pipelineMetrics, error) {
	meter := mp.Meter("github.com/go-faster/scpbot/pipeline")

	documents, err := meter.Int64Counter(
		"scpbot.pipeline.documents",
		metric.WithDescription("Count of processed documents by source and status"),
	)
	if err != nil {
		return nil, err
	}

	chunks, err := meter.Int64Counter(
		"scpbot.pipeline.chunks",
		metric.WithDescription("Count of chunks by embedding status"),
	)
	if err != nil {
		return nil, err
	}

	embedDur, err := meter.Float64Histogram(
		"scpbot.pipeline.embed.duration",
		metric.WithDescription("Duration of embedding operation"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}

	return &pipelineMetrics{
		documents: documents,
		chunks:    chunks,
		embedDur:  embedDur,
	}, nil
}
