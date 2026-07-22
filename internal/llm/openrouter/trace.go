package openrouter

import (
	"context"

	"github.com/openai/openai-go/v3/option"
	"go.opentelemetry.io/otel/trace"
)

// traceLinkOptions links the OpenRouter request to the current OpenTelemetry
// span, so the provider-side trace OpenRouter records can be correlated with
// ours. OpenRouter reads a top-level `trace` object with `trace_id` and
// `parent_span_id` from the request body — an unusual place to carry trace
// context, but it's what their broadcast/external-traces feature expects:
// https://openrouter.ai/docs/guides/features/broadcast#linking-to-external-traces
//
// Returns nil when the context has no valid span, so no `trace` field is sent.
func traceLinkOptions(ctx context.Context) []option.RequestOption {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return nil
	}
	return []option.RequestOption{
		option.WithJSONSet("trace", map[string]string{
			"trace_id":       sc.TraceID().String(),
			"parent_span_id": sc.SpanID().String(),
		}),
	}
}
