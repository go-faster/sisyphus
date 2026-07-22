package agent

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// toolListFailures counts Engine.Run's toolSource.Tools failures — the
// degrade-instead-of-fail path (see engine.go). Package-level and lazily
// initialized off the global MeterProvider rather than threaded through
// NewEngine/NewLoop/NewContextLoop's constructors: Engine[T] is shared by
// every agent (/investigate's Loop, /context's ContextLoop) across two
// binaries (ssagent, ssapi), both of which already wire up global OTel via
// go-faster/sdk's app.Run, so there is no per-caller configuration this
// needs. Without a counter here, a stuck-degraded MCP gateway/sandbox looks
// identical to a healthy one in success/error rate dashboards — only a log
// line (easy to miss) marks it.
var (
	toolListFailuresOnce sync.Once
	toolListFailures     metric.Int64Counter
)

func recordToolListFailure(ctx context.Context) {
	toolListFailuresOnce.Do(func() {
		toolListFailures, _ = otel.GetMeterProvider().
			Meter("github.com/go-faster/sisyphus/agent").
			Int64Counter(
				"sisyphus.agent.tool_list_failures",
				metric.WithDescription("Count of Engine.Run continuing without tools after a toolSource.Tools failure"),
			)
	})
	if toolListFailures != nil {
		toolListFailures.Add(ctx, 1)
	}
}
