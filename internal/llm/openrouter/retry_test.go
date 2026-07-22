package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

const choiceEmbeddedError = `{"choices":[{"message":{"role":"assistant","content":"Connect timeout, please try again later."},"finish_reason":"error","error":{"code":502,"message":"Connect timeout, please try again later."}}]}`

func okCompletion(t *testing.T) []byte {
	t.Helper()
	b, err := json.Marshal(openai.ChatCompletion{
		Choices: []openai.ChatCompletionChoice{
			{Message: openai.ChatCompletionMessage{Content: "recovered"}},
		},
		Usage: openai.CompletionUsage{PromptTokens: 10, CompletionTokens: 2},
	})
	require.NoError(t, err)
	return b
}

// fastRetryClient builds a Client with negligible backoff so retry tests don't
// sleep for real.
func fastRetryClient(srv *httptest.Server) *Client {
	return New("test-key", Options{BaseURL: srv.URL, RetryBackoff: time.Nanosecond})
}

func TestCompleteWithTools_RetriesUpstreamErrorThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// HTTP 200 both times — the error is inside the body (OpenRouter's
		// provider-failed-mid-generation shape), which the SDK does not retry.
		if calls.Add(1) == 1 {
			_, _ = io.WriteString(w, choiceEmbeddedError)
			return
		}
		_, _ = w.Write(okCompletion(t))
	}))
	t.Cleanup(srv.Close)

	msg, _, err := fastRetryClient(srv).CompleteWithTools(context.Background(), "m", nil, nil)
	require.NoError(t, err)
	require.Equal(t, "recovered", msg.Content)
	require.Equal(t, int32(2), calls.Load(), "should have retried once")
}

func TestCompleteWithTools_UpstreamErrorExhausted(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, choiceEmbeddedError)
	}))
	t.Cleanup(srv.Close)

	_, _, err := fastRetryClient(srv).CompleteWithTools(context.Background(), "m", nil, nil)
	require.Error(t, err)
	var ue *UpstreamError
	require.True(t, errors.As(err, &ue), "want *UpstreamError, got %v", err)
	require.Equal(t, 502, ue.Code)
	// default MaxRetries=2 -> 3 total attempts.
	require.Equal(t, int32(3), calls.Load())
}

func TestCompleteWithTools_LinksTrace(t *testing.T) {
	var body atomic.Pointer[map[string]any]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&m))
		body.Store(&m)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(okCompletion(t))
	}))
	t.Cleanup(srv.Close)

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x9a, 0x12, 0x0d, 0xef, 0xe7, 0x4f, 0xe2, 0x49, 0xcb, 0x6c, 0x56, 0xaa, 0x6d, 0x8b, 0xc9, 0x75},
		SpanID:     trace.SpanID{0xf1, 0xc8, 0xcf, 0xb3, 0x5f, 0x6b, 0xee, 0x1f},
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	_, _, err := fastRetryClient(srv).CompleteWithTools(ctx, "m", nil, nil)
	require.NoError(t, err)

	m := body.Load()
	require.NotNil(t, m)
	tr, ok := (*m)["trace"].(map[string]any)
	require.True(t, ok, "request body missing trace object: %v", *m)
	require.Equal(t, sc.TraceID().String(), tr["trace_id"])
	require.Equal(t, sc.SpanID().String(), tr["parent_span_id"])
}

func TestCompleteWithTools_NoTraceWithoutSpan(t *testing.T) {
	var body atomic.Pointer[map[string]any]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&m))
		body.Store(&m)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(okCompletion(t))
	}))
	t.Cleanup(srv.Close)

	_, _, err := fastRetryClient(srv).CompleteWithTools(context.Background(), "m", nil, nil)
	require.NoError(t, err)

	m := body.Load()
	require.NotNil(t, m)
	_, ok := (*m)["trace"]
	require.False(t, ok, "trace field should be absent without a valid span")
}
