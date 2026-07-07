package netclient

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type clientMetrics struct {
	requests    metric.Int64Counter
	errors      metric.Int64Counter
	duration    metric.Float64Histogram
	cacheStatus metric.Int64Counter
}

func newClientMetrics(meterProvider metric.MeterProvider) (*clientMetrics, error) {
	meter := meterProvider.Meter("github.com/go-faster/sisyphus/netclient")
	requests, err := meter.Int64Counter(
		"sisyphus.netclient.requests",
		metric.WithDescription("Count of outbound HTTP requests per client and status code"),
	)
	if err != nil {
		return nil, err
	}
	errors, err := meter.Int64Counter(
		"sisyphus.netclient.errors",
		metric.WithDescription("Count of outbound HTTP transport errors per client and type"),
	)
	if err != nil {
		return nil, err
	}
	duration, err := meter.Float64Histogram(
		"sisyphus.netclient.duration",
		metric.WithDescription("Duration of outbound HTTP requests"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}
	cacheStatus, err := meter.Int64Counter(
		"sisyphus.netclient.cache",
		metric.WithDescription("Count of outbound HTTP cache status (hit, miss, bypass)"),
	)
	if err != nil {
		return nil, err
	}
	return &clientMetrics{
		requests:    requests,
		errors:      errors,
		duration:    duration,
		cacheStatus: cacheStatus,
	}, nil
}

func (m *clientMetrics) record(ctx context.Context, clientName string, statusCode int, durSeconds float64) {
	m.requests.Add(ctx, 1,
		metric.WithAttributes(
			attribute.String("client_name", clientName),
			attribute.Int("status_code", statusCode),
		),
	)
	m.duration.Record(ctx, durSeconds,
		metric.WithAttributes(
			attribute.String("client_name", clientName),
			attribute.String("status_class", statusClass(statusCode)),
		),
	)
}

func (m *clientMetrics) recordError(ctx context.Context, clientName, errorType string, durSeconds float64) {
	m.errors.Add(ctx, 1,
		metric.WithAttributes(
			attribute.String("client_name", clientName),
			attribute.String("error_type", errorType),
		),
	)
	m.duration.Record(ctx, durSeconds,
		metric.WithAttributes(
			attribute.String("client_name", clientName),
			attribute.String("status_class", "error"),
		),
	)
}

func (m *clientMetrics) recordCache(ctx context.Context, clientName, status string) {
	if m.cacheStatus != nil {
		m.cacheStatus.Add(ctx, 1,
			metric.WithAttributes(
				attribute.String("client_name", clientName),
				attribute.String("status", status),
			),
		)
	}
}

func statusClass(statusCode int) string {
	if statusCode < 100 {
		return "unknown"
	}
	return string(rune('0'+statusCode/100)) + "xx"
}
