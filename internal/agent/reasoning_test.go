package agent

import (
	"encoding/json"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/stretchr/testify/require"
)

func TestExtractReasoning(t *testing.T) {
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
			require.Equal(t, tt.want, ExtractReasoning(msg))
		})
	}
}

// TestToParamWithReasoning verifies the model's reasoning trace survives the
// round trip into the next request's messages — msg.ToParam alone drops it,
// since it only knows the fields the OpenAI schema defines.
func TestToParamWithReasoning(t *testing.T) {
	var msg openai.ChatCompletionMessage
	require.NoError(t, json.Unmarshal([]byte(
		`{"role":"assistant","content":"hi","reasoning":"first I checked the RFC"}`,
	), &msg))

	param := toParamWithReasoning(msg)
	require.NotNil(t, param.OfAssistant)

	data, err := json.Marshal(param)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, "first I checked the RFC", decoded["reasoning"])
}

// TestToParamWithReasoning_Absent verifies a completion with no reasoning
// round-trips normally, with no stray "reasoning" key added.
func TestToParamWithReasoning_Absent(t *testing.T) {
	var msg openai.ChatCompletionMessage
	require.NoError(t, json.Unmarshal([]byte(`{"role":"assistant","content":"hi"}`), &msg))

	param := toParamWithReasoning(msg)
	data, err := json.Marshal(param)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.NotContains(t, decoded, "reasoning")
}
