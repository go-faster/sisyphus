package main

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type agentMetrics struct {
	requests    metric.Int64Counter
	duration    metric.Float64Histogram
	toolsUsed   metric.Int64Histogram
	reportChars metric.Int64Histogram
}

func newAgentMetrics(mp metric.MeterProvider) (*agentMetrics, error) {
	meter := mp.Meter("github.com/go-faster/sisyphus/ssagent")
	requests, err := meter.Int64Counter(
		"sisyphus.agent.investigate.requests",
		metric.WithDescription("Count of ssagent investigation requests by status and verdict"),
	)
	if err != nil {
		return nil, err
	}
	duration, err := meter.Float64Histogram(
		"sisyphus.agent.investigate.duration",
		metric.WithDescription("Duration of ssagent investigation requests"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}
	toolsUsed, err := meter.Int64Histogram(
		"sisyphus.agent.investigate.tools_used",
		metric.WithDescription("MCP tools used per ssagent investigation"),
	)
	if err != nil {
		return nil, err
	}
	reportChars, err := meter.Int64Histogram(
		"sisyphus.agent.investigate.report_chars",
		metric.WithDescription("Report size in characters per ssagent investigation"),
	)
	if err != nil {
		return nil, err
	}
	return &agentMetrics{
		requests:    requests,
		duration:    duration,
		toolsUsed:   toolsUsed,
		reportChars: reportChars,
	}, nil
}

func (m *agentMetrics) record(ctx context.Context, status, verdict string, durSeconds float64, toolsUsed, reportChars int) {
	if verdict == "" {
		verdict = "unknown"
	}
	attrs := metric.WithAttributes(
		attribute.String("status", status),
		attribute.String("verdict", verdict),
	)
	m.requests.Add(ctx, 1, attrs)
	m.duration.Record(ctx, durSeconds, attrs)
	if status == "ok" {
		m.toolsUsed.Record(ctx, int64(toolsUsed), metric.WithAttributes(attribute.String("verdict", verdict)))
		m.reportChars.Record(ctx, int64(reportChars), metric.WithAttributes(attribute.String("verdict", verdict)))
	}
}
