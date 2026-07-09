package webhook

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// metrics holds the OTel instruments shared by Trigger, Poller, and the
// webhook HTTP handlers.
type metrics struct {
	requests  metric.Int64Counter     // webhook HTTP requests received, by provider+result
	fires     metric.Int64Counter     // Trigger.Fire calls received, by key
	runs      metric.Int64Counter     // debounced ingestion runs executed, by key+status
	runDur    metric.Float64Histogram // duration of a debounced ingestion run, by key+status
	pollTicks metric.Int64Counter     // poller ticks fired, by key
}

func newMetrics(mp metric.MeterProvider) (*metrics, error) {
	meter := mp.Meter("github.com/go-faster/sisyphus/webhook")

	requests, err := meter.Int64Counter(
		"sisyphus.webhook.requests",
		metric.WithDescription("Webhook HTTP requests received, by provider and result"),
	)
	if err != nil {
		return nil, err
	}
	fires, err := meter.Int64Counter(
		"sisyphus.webhook.trigger.fires",
		metric.WithDescription("Trigger.Fire calls received, by key"),
	)
	if err != nil {
		return nil, err
	}
	runs, err := meter.Int64Counter(
		"sisyphus.webhook.trigger.runs",
		metric.WithDescription("Debounced ingestion runs executed, by key and status"),
	)
	if err != nil {
		return nil, err
	}
	runDur, err := meter.Float64Histogram(
		"sisyphus.webhook.trigger.run.duration",
		metric.WithDescription("Duration of a debounced ingestion run"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}
	pollTicks, err := meter.Int64Counter(
		"sisyphus.webhook.poll.ticks",
		metric.WithDescription("Poller ticks fired, by key"),
	)
	if err != nil {
		return nil, err
	}

	return &metrics{
		requests:  requests,
		fires:     fires,
		runs:      runs,
		runDur:    runDur,
		pollTicks: pollTicks,
	}, nil
}

func (m *metrics) recordRequest(ctx context.Context, provider, result string) {
	if m == nil {
		return
	}
	m.requests.Add(ctx, 1, metric.WithAttributes(
		attribute.String("provider", provider),
		attribute.String("result", result),
	))
}

func (m *metrics) recordFire(ctx context.Context, key string) {
	if m == nil {
		return
	}
	m.fires.Add(ctx, 1, metric.WithAttributes(attribute.String("key", key)))
}

func (m *metrics) recordRun(ctx context.Context, key string, durSeconds float64, err error) {
	if m == nil {
		return
	}
	status := "ok"
	if err != nil {
		status = "error"
	}
	attrs := metric.WithAttributes(
		attribute.String("key", key),
		attribute.String("status", status),
	)
	m.runs.Add(ctx, 1, attrs)
	m.runDur.Record(ctx, durSeconds, attrs)
}

func (m *metrics) recordPollTick(ctx context.Context, key string) {
	if m == nil {
		return
	}
	m.pollTicks.Add(ctx, 1, metric.WithAttributes(attribute.String("key", key)))
}
