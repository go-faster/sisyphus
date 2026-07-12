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

func TestCompleteWithTools(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var req struct {
			Tools []json.RawMessage `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode error: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		require.Len(t, req.Tools, 1)

		resp := openai.ChatCompletion{
			Choices: []openai.ChatCompletionChoice{
				{
					Message: openai.ChatCompletionMessage{
						Content: "tool call requested",
						ToolCalls: []openai.ChatCompletionMessageToolCallUnion{
							{
								ID:   "call_123",
								Type: "function",
								Function: openai.ChatCompletionMessageFunctionToolCallFunction{
									Name:      "test_tool",
									Arguments: `{"arg":"value"}`,
								},
							},
						},
					},
				},
			},
			Usage: openai.CompletionUsage{
				PromptTokens:     10,
				CompletionTokens: 5,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)

	c := newClient(t, srv)
	msg, usage, err := c.CompleteWithTools(context.Background(), "test-model", []openai.ChatCompletionMessageParamUnion{
		openai.UserMessage("test request"),
	}, []openai.ChatCompletionToolUnionParam{
		{
			OfFunction: &openai.ChatCompletionFunctionToolParam{
				Function: openai.FunctionDefinitionParam{
					Name: "test_tool",
				},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "tool call requested", msg.Content)
	require.Len(t, msg.ToolCalls, 1)
	require.Equal(t, "call_123", msg.ToolCalls[0].ID)
	require.Equal(t, "test_tool", msg.ToolCalls[0].Function.Name)
	require.Equal(t, int64(10), usage.PromptTokens)
	require.Equal(t, int64(5), usage.CompletionTokens)
}
