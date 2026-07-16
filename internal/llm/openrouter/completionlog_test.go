package openrouter

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-faster/sdk/zctx"
	"github.com/openai/openai-go/v3"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// TestCompleteWithTools_LogsCompletion pins the whole path: a provider
// response carrying `reasoning` must reach the log via the ctx logger.
func TestCompleteWithTools_LogsCompletion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Written as a literal: `reasoning` is an OpenRouter extension absent
		// from openai-go's response struct, so it cannot be round-tripped
		// through openai.ChatCompletion.
		_, _ = w.Write([]byte(`{
			"choices": [{
				"message": {
					"role": "assistant",
					"content": "the answer",
					"reasoning": "first I checked the RFC",
					"tool_calls": [{
						"id": "call_1",
						"type": "function",
						"function": {"name": "search_knowledge", "arguments": "{}"}
					}]
				}
			}],
			"usage": {"prompt_tokens": 10, "completion_tokens": 5}
		}`))
	}))
	defer srv.Close()

	core, logs := observer.New(zapcore.DebugLevel)
	ctx := zctx.Base(t.Context(), zap.New(core))

	c := New("test-key", Options{BaseURL: srv.URL})
	_, usage, err := c.CompleteWithTools(ctx, "test-model", nil, nil)
	require.NoError(t, err)
	require.Equal(t, int64(10), usage.PromptTokens)

	entries := logs.FilterMessage("llm completion").All()
	require.Len(t, entries, 1)
	fields := entries[0].ContextMap()
	require.Equal(t, "first I checked the RFC", fields["reasoning"])
	require.Equal(t, "the answer", fields["content"])
	require.Equal(t, "test-model", fields["model"])
	require.Equal(t, []any{"search_knowledge"}, fields["tool_calls"])
}

// TestCompleteWithTools_NoLogBelowDebug guards the cost side: the payload is
// large, so it must not be assembled when debug logging is off.
func TestCompleteWithTools_NoLogBelowDebug(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi","reasoning":"secret"}}]}`))
	}))
	defer srv.Close()

	core, logs := observer.New(zapcore.InfoLevel)
	ctx := zctx.Base(t.Context(), zap.New(core))

	c := New("test-key", Options{BaseURL: srv.URL})
	_, _, err := c.CompleteWithTools(ctx, "test-model", nil, nil)
	require.NoError(t, err)
	require.Zero(t, logs.FilterMessage("llm completion").Len())
}

func TestCompletionReasoning(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "absent",
			raw:  `{"role":"assistant","content":"hi"}`,
			want: "",
		},
		{
			name: "null",
			raw:  `{"role":"assistant","content":"hi","reasoning":null}`,
			want: "",
		},
		{
			name: "string",
			raw:  `{"role":"assistant","content":"hi","reasoning":"first I checked the RFC"}`,
			want: "first I checked the RFC",
		},
		{
			name: "empty string",
			raw:  `{"role":"assistant","content":"hi","reasoning":""}`,
			want: "",
		},
		{
			// Some providers return structured reasoning blocks rather than a
			// bare string; fall back to the raw JSON instead of dropping it.
			name: "structured falls back to raw",
			raw:  `{"role":"assistant","content":"hi","reasoning":[{"text":"step one"}]}`,
			want: `[{"text":"step one"}]`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var msg openai.ChatCompletionMessage
			require.NoError(t, json.Unmarshal([]byte(tt.raw), &msg))
			require.Equal(t, tt.want, completionReasoning(msg))
		})
	}
}
