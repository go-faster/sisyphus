package retrieval

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type retrievalMetrics struct {
	searches  metric.Int64Counter
	searchDur metric.Float64Histogram
}

func newRetrievalMetrics(mp metric.MeterProvider) (*retrievalMetrics, error) {
	meter := mp.Meter("github.com/go-faster/scpbot/retrieval")

	searches, err := meter.Int64Counter(
		"scpbot.retrieval.searches",
		metric.WithDescription("Count of searches per backend and status"),
	)
	if err != nil {
		return nil, err
	}

	searchDur, err := meter.Float64Histogram(
		"scpbot.retrieval.search.duration",
		metric.WithDescription("Duration of retrieval search"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}

	return &retrievalMetrics{
		searches:  searches,
		searchDur: searchDur,
	}, nil
}

func (m *retrievalMetrics) recordSearch(ctx context.Context, backend string, err error) {
	status := "ok"
	if err != nil {
		status = "error"
	}
	m.searches.Add(ctx, 1,
		metric.WithAttributes(
			attribute.String("backend", backend),
			attribute.String("status", status),
		),
	)
}
