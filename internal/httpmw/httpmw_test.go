package httpmw

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-faster/sdk/zctx"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestInjectLogger(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	h := InjectLogger(zap.New(core))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		zctx.From(r.Context()).Info("handler log")
	}))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", http.NoBody))

	entries := logs.FilterMessage("handler log").All()
	require.Len(t, entries, 1)
}

func TestLoggingUsesContextLogger(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	h := InjectLogger(zap.New(core))(Logging()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health", http.NoBody))

	entries := logs.FilterMessage("got http request").All()
	require.Len(t, entries, 1)
	require.EqualValues(t, http.StatusNoContent, entries[0].ContextMap()["status"])
}

func TestLoggingIncludesTraceContext(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	h := InjectLogger(zap.New(core))(Logging()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})))

	traceID := trace.TraceID{1, 2, 3}
	spanID := trace.SpanID{4, 5, 6}
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID,
		SpanID:  spanID,
	})
	req := httptest.NewRequest(http.MethodGet, "/health", http.NoBody)
	req = req.WithContext(trace.ContextWithSpanContext(req.Context(), sc))
	h.ServeHTTP(httptest.NewRecorder(), req)

	entries := logs.FilterMessage("got http request").All()
	require.Len(t, entries, 1)
	require.Contains(t, entries[0].ContextMap(), "ctx")
}
