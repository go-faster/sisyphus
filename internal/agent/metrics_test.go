package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// TestRecordToolListFailure verifies the counter is actually wired to the
// global MeterProvider and increments on each call — sync.Once means the
// instrument is built only once across the whole test binary, so this
// doesn't install its own provider (that would race with other tests using
// the global); it just checks the exported metric moves.
func TestRecordToolListFailure(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() { otel.SetMeterProvider(prev) })

	// Force the lazy instrument to (re-)bind against the test provider.
	toolListFailuresOnce = sync.Once{}

	recordToolListFailure(context.Background())
	recordToolListFailure(context.Background())

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	var got int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "sisyphus.agent.tool_list_failures" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			require.True(t, ok)
			for _, dp := range sum.DataPoints {
				got += dp.Value
			}
		}
	}
	require.Equal(t, int64(2), got)
}
