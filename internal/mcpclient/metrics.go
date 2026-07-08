package mcpclient

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type mcpMetrics struct {
	calls metric.Int64Counter
	dur   metric.Float64Histogram
}

func newMCPMetrics(mp metric.MeterProvider) (*mcpMetrics, error) {
	meter := mp.Meter("github.com/go-faster/sisyphus/mcpclient")
	calls, err := meter.Int64Counter(
		"sisyphus.mcpclient.calls",
		metric.WithDescription("MCP tool calls"),
	)
	if err != nil {
		return nil, err
	}
	dur, err := meter.Float64Histogram(
		"sisyphus.mcpclient.duration",
		metric.WithDescription("MCP tool call duration"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}
	return &mcpMetrics{calls: calls, dur: dur}, nil
}

func (m *mcpMetrics) record(ctx context.Context, toolName string, durSeconds float64, err error) {
	status := "ok"
	if err != nil {
		status = "error"
	}
	m.calls.Add(ctx, 1,
		metric.WithAttributes(
			attribute.String("tool", toolName),
			attribute.String("status", status),
		),
	)
	m.dur.Record(ctx, durSeconds,
		metric.WithAttributes(
			attribute.String("tool", toolName),
			attribute.String("status", status),
		),
	)
}
