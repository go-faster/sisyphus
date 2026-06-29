package netclient

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type clientMetrics struct {
	requests metric.Int64Counter
}

func newClientMetrics(meterProvider metric.MeterProvider) (*clientMetrics, error) {
	meter := meterProvider.Meter("github.com/go-faster/scpbot/netclient")
	requests, err := meter.Int64Counter(
		"scpbot.netclient.requests",
		metric.WithDescription("Count of outbound HTTP requests per client and status code"),
	)
	if err != nil {
		return nil, err
	}
	return &clientMetrics{
		requests: requests,
	}, nil
}

func (m *clientMetrics) record(ctx context.Context, clientName string, statusCode int) {
	m.requests.Add(ctx, 1,
		metric.WithAttributes(
			attribute.String("client_name", clientName),
			attribute.Int("status_code", statusCode),
		),
	)
}
