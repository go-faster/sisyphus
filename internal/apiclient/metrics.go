package apiclient

import (
	"context"

	"github.com/go-faster/sdk/autometric"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type clientMetrics struct {
	Requests metric.Int64Counter     `description:"Count of API client requests by operation and status"`
	Duration metric.Float64Histogram `description:"Duration of API client requests" unit:"s"`
	Results  metric.Int64Counter     `description:"Count of retrieval results returned by API client requests"`
}

func newClientMetrics(mp metric.MeterProvider) (*clientMetrics, error) {
	metrics := clientMetrics{}
	if err := autometric.Init(mp.Meter("github.com/go-faster/sisyphus/apiclient"), &metrics, autometric.InitOptions{
		Prefix: "sisyphus.apiclient.",
	}); err != nil {
		return nil, err
	}
	return &metrics, nil
}

func (m *clientMetrics) record(ctx context.Context, op string, durSeconds float64, resultCount int, err error) {
	status := "ok"
	if err != nil {
		status = "error"
	}
	attrs := metric.WithAttributes(
		attribute.String("operation", op),
		attribute.String("status", status),
	)
	m.Requests.Add(ctx, 1, attrs)
	m.Duration.Record(ctx, durSeconds, attrs)
	if resultCount > 0 {
		m.Results.Add(ctx, int64(resultCount), metric.WithAttributes(attribute.String("operation", op)))
	}
}
