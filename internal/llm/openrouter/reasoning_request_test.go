package openrouter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/stretchr/testify/require"
)

// TestCompleteWithTools_ReasoningEffort verifies Options.ReasoningEffort
// actually reaches the request body as OpenRouter's unified reasoning
// param — without it, whether a completion carries a reasoning trace to
// round-trip (internal/agent/reasoning.go) is left entirely to whichever
// upstream provider OpenRouter happens to route to.
func TestCompleteWithTools_ReasoningEffort(t *testing.T) {
	var gotReasoning json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Reasoning json.RawMessage `json:"reasoning"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		gotReasoning = req.Reasoning

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(openai.ChatCompletion{
			Choices: []openai.ChatCompletionChoice{{Message: openai.ChatCompletionMessage{Content: "hi"}}},
		})
	}))
	t.Cleanup(srv.Close)

	c := New("test-key", Options{BaseURL: srv.URL, ReasoningEffort: "low"})
	_, _, err := c.CompleteWithTools(context.Background(), "test-model", []openai.ChatCompletionMessageParamUnion{
		openai.UserMessage("hi"),
	}, nil)
	require.NoError(t, err)
	require.JSONEq(t, `{"effort":"low"}`, string(gotReasoning))
}

// TestCompleteWithTools_ReasoningEffortUnset verifies the request carries no
// "reasoning" field at all when ReasoningEffort is empty, matching prior
// behavior for operators who don't opt in.
func TestCompleteWithTools_ReasoningEffortUnset(t *testing.T) {
	var raw map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&raw))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(openai.ChatCompletion{
			Choices: []openai.ChatCompletionChoice{{Message: openai.ChatCompletionMessage{Content: "hi"}}},
		})
	}))
	t.Cleanup(srv.Close)

	c := New("test-key", Options{BaseURL: srv.URL})
	_, _, err := c.CompleteWithTools(context.Background(), "test-model", []openai.ChatCompletionMessageParamUnion{
		openai.UserMessage("hi"),
	}, nil)
	require.NoError(t, err)
	require.NotContains(t, raw, "reasoning")
}
