package git

import (
	"go.opentelemetry.io/otel/metric"
)

// walkMetrics counts files seen during a walk by repo and outcome, so a
// silently-skipped extension (e.g. an unrecognized doc format) shows up in
// telemetry instead of just an empty diff in Postgres.
type walkMetrics struct {
	files metric.Int64Counter
}

func newWalkMetrics(mp metric.MeterProvider) (*walkMetrics, error) {
	meter := mp.Meter("github.com/go-faster/sisyphus/ingest/git")

	files, err := meter.Int64Counter(
		"sisyphus.ingest.git.walk.files",
		metric.WithDescription("Count of files seen while walking a git source, by repo and outcome"),
	)
	if err != nil {
		return nil, err
	}

	return &walkMetrics{files: files}, nil
}
