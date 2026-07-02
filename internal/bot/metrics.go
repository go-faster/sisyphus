package bot

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type botMetrics struct {
	requests metric.Int64Counter
	duration metric.Float64Histogram
	results  metric.Int64Counter
}

func newBotMetrics(mp metric.MeterProvider) (*botMetrics, error) {
	meter := mp.Meter("github.com/go-faster/sisyphus/bot")
	requests, err := meter.Int64Counter(
		"sisyphus.bot.context.requests",
		metric.WithDescription("Count of Telegram context requests by status"),
	)
	if err != nil {
		return nil, err
	}
	duration, err := meter.Float64Histogram(
		"sisyphus.bot.context.duration",
		metric.WithDescription("Duration of Telegram context requests"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}
	results, err := meter.Int64Counter(
		"sisyphus.bot.context.results",
		metric.WithDescription("Count of retrieval results used by Telegram context requests"),
	)
	if err != nil {
		return nil, err
	}
	return &botMetrics{requests: requests, duration: duration, results: results}, nil
}

func (m *botMetrics) recordContext(ctx context.Context, durSeconds float64, resultCount int, err error) {
	status := "ok"
	if err != nil {
		status = "error"
	}
	attrs := metric.WithAttributes(attribute.String("status", status))
	m.requests.Add(ctx, 1, attrs)
	m.duration.Record(ctx, durSeconds, attrs)
	if err == nil {
		m.results.Add(ctx, int64(resultCount))
	}
}
