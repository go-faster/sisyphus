package openrouter

import (
	"encoding/json"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/index"
)

func TestParseSubmitAnswer(t *testing.T) {
	allowed := map[string]struct{}{
		"https://jira/IDP-1":  {},
		"https://grafana/d/1": {},
	}

	t.Run("tool call keeps only allowed valid buttons", func(t *testing.T) {
		args := mustMarshal(t, submitAnswerArgs{
			Answer: "  the answer  ",
			Buttons: []index.Link{
				{Text: "Ticket", URL: "https://jira/IDP-1"},
				{Text: "Hallucinated", URL: "https://evil/phish"}, // not in allowed set
				{Text: "no scheme", URL: "jira/IDP-1"},            // invalid
				{Text: "dup", URL: "https://jira/IDP-1"},          // duplicate URL
				{Text: "Dash", URL: "https://grafana/d/1"},
			},
		})
		msg := openai.ChatCompletionMessage{
			ToolCalls: []openai.ChatCompletionMessageToolCallUnion{{
				Type: "function",
				Function: openai.ChatCompletionMessageFunctionToolCallFunction{
					Name:      submitAnswerToolName,
					Arguments: args,
				},
			}},
		}
		got := parseSubmitAnswer(msg, allowed)
		require.Equal(t, "the answer", got.Text)
		require.Equal(t, []index.Link{
			{Text: "Ticket", URL: "https://jira/IDP-1"},
			{Text: "Dash", URL: "https://grafana/d/1"},
		}, got.Links)
	})

	t.Run("falls back to prose content when no tool call", func(t *testing.T) {
		msg := openai.ChatCompletionMessage{Content: "  plain prose answer  "}
		got := parseSubmitAnswer(msg, allowed)
		require.Equal(t, "plain prose answer", got.Text)
		require.Empty(t, got.Links)
	})
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}
