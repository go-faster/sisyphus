package anydoc

import (
	"go.opentelemetry.io/otel/metric"
)

// converterMetrics counts conversions by format and status, so a format that
// started failing is visible without diffing Postgres. Status is "ok" or the
// [Error] Code, which is what an alert on failing conversion keys on.
type converterMetrics struct {
	documents metric.Int64Counter
	duration  metric.Float64Histogram
}

func newConverterMetrics(mp metric.MeterProvider) (*converterMetrics, error) {
	meter := mp.Meter("github.com/go-faster/sisyphus/convert/anydoc")

	documents, err := meter.Int64Counter(
		"sisyphus.convert.documents",
		metric.WithDescription("Count of attempted document conversions by format and status"),
	)
	if err != nil {
		return nil, err
	}

	duration, err := meter.Float64Histogram(
		"sisyphus.convert.duration",
		metric.WithDescription("Duration of one document conversion, subprocess included"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}

	return &converterMetrics{documents: documents, duration: duration}, nil
}
