package retrieval

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type retrievalMetrics struct {
	searches       metric.Int64Counter
	searchDur      metric.Float64Histogram
	backendDur     metric.Float64Histogram
	backendResults metric.Int64Counter
	results        metric.Int64Counter
	emptyResults   metric.Int64Counter
}

func newRetrievalMetrics(mp metric.MeterProvider) (*retrievalMetrics, error) {
	meter := mp.Meter("github.com/go-faster/sisyphus/retrieval")

	searches, err := meter.Int64Counter(
		"sisyphus.retrieval.searches",
		metric.WithDescription("Count of searches per backend and status"),
	)
	if err != nil {
		return nil, err
	}

	searchDur, err := meter.Float64Histogram(
		"sisyphus.retrieval.search.duration",
		metric.WithDescription("Duration of retrieval search"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}
	backendDur, err := meter.Float64Histogram(
		"sisyphus.retrieval.backend.duration",
		metric.WithDescription("Duration of retrieval backend searches"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}
	backendResults, err := meter.Int64Counter(
		"sisyphus.retrieval.backend.results",
		metric.WithDescription("Count of retrieval results returned by backend"),
	)
	if err != nil {
		return nil, err
	}
	results, err := meter.Int64Counter(
		"sisyphus.retrieval.results",
		metric.WithDescription("Count of final merged retrieval results"),
	)
	if err != nil {
		return nil, err
	}
	emptyResults, err := meter.Int64Counter(
		"sisyphus.retrieval.empty_results",
		metric.WithDescription("Count of retrieval calls that returned no results"),
	)
	if err != nil {
		return nil, err
	}

	return &retrievalMetrics{
		searches:       searches,
		searchDur:      searchDur,
		backendDur:     backendDur,
		backendResults: backendResults,
		results:        results,
		emptyResults:   emptyResults,
	}, nil
}

func (m *retrievalMetrics) recordBackend(ctx context.Context, backend string, resultCount int, durationSeconds float64, err error) {
	status := "ok"
	if err != nil {
		status = "error"
	}
	attrs := metric.WithAttributes(
		attribute.String("backend", backend),
		attribute.String("status", status),
	)
	m.searches.Add(ctx, 1,
		attrs,
	)
	m.backendDur.Record(ctx, durationSeconds,
		attrs,
	)
	if err == nil {
		m.backendResults.Add(ctx, int64(resultCount),
			metric.WithAttributes(attribute.String("backend", backend)),
		)
	}
}

func (m *retrievalMetrics) recordResults(ctx context.Context, count int) {
	m.results.Add(ctx, int64(count))
	if count == 0 {
		m.emptyResults.Add(ctx, 1)
	}
}
