package anydoc

import (
	"testing"

	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// The status attribute is what an alert on failing conversion keys on, so it
// has to carry the Error Code rather than a flat "error".
func TestMetricsRecordStatusPerCode(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	conv := newStubConverter(t, Options{
		MeterProvider: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)),
	})

	_, err := conv.Convert(t.Context(), stubPath(t, "ok"))
	require.NoError(t, err)
	_, err = conv.Convert(t.Context(), stubPath(t, "encrypted"))
	require.Error(t, err)

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(t.Context(), &rm))

	counts := map[string]int64{}
	var durations int
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch m.Name {
			case "sisyphus.convert.documents":
				sum, ok := m.Data.(metricdata.Sum[int64])
				require.True(t, ok)
				for _, dp := range sum.DataPoints {
					status, found := dp.Attributes.Value("status")
					require.True(t, found)
					format, found := dp.Attributes.Value("format")
					require.True(t, found)
					require.Equal(t, "docx", format.AsString())
					counts[status.AsString()] += dp.Value
				}
			case "sisyphus.convert.duration":
				hist, ok := m.Data.(metricdata.Histogram[float64])
				require.True(t, ok)
				durations = len(hist.DataPoints)
			}
		}
	}

	require.Equal(t, map[string]int64{"ok": 1, CodeEncrypted: 1}, counts)
	require.Equal(t, 2, durations, "one duration series per status")
}
