package maint

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Run outcomes, as the `status` attribute on the run instruments.
const (
	statusOK       = "ok"
	statusError    = "error"
	statusSkipped  = "skipped"
	statusCanceled = "canceled"
)

// metrics holds the OTel instruments a Scheduler records.
//
// These are deliberately their own namespace rather than internal/webhook's:
// a garbage collection sweep is not webhook activity, and reporting it as such
// makes both signals unreadable.
type metrics struct {
	runs   metric.Int64Counter     // maintenance runs executed, by job+status
	runDur metric.Float64Histogram // duration of a maintenance run, by job+status
}

func newMetrics(mp metric.MeterProvider) (*metrics, error) {
	meter := mp.Meter("github.com/go-faster/sisyphus/maint")

	runs, err := meter.Int64Counter(
		"sisyphus.maint.runs",
		metric.WithDescription("Maintenance runs executed, by job and status"),
	)
	if err != nil {
		return nil, err
	}
	runDur, err := meter.Float64Histogram(
		"sisyphus.maint.run.duration",
		metric.WithDescription("Duration of a maintenance run"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}
	return &metrics{runs: runs, runDur: runDur}, nil
}

func (m *metrics) recordRun(ctx context.Context, job, status string, durSeconds float64) {
	if m == nil {
		return
	}
	attrs := metric.WithAttributes(
		attribute.String("job", job),
		attribute.String("status", status),
	)
	m.runs.Add(ctx, 1, attrs)
	m.runDur.Record(ctx, durSeconds, attrs)
}
