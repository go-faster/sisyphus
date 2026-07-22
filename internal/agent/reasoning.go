package agent

import (
	"encoding/json"

	"github.com/openai/openai-go/v3"
)

// toParamWithReasoning converts a completion into the param form fed back
// into the next request, carrying over the model's reasoning trace when the
// provider returned one. msg.ToParam alone drops it: it only copies fields
// the OpenAI schema knows about, and reasoning is a non-standard extension
// (OpenRouter/DeepSeek et al. park it in JSON.ExtraFields). Without this the
// model loses its own chain of thought between loop iterations and re-derives
// a plan from scratch every turn instead of continuing it.
func toParamWithReasoning(msg openai.ChatCompletionMessage) openai.ChatCompletionMessageParamUnion {
	param := msg.ToParam()
	if reasoning := ExtractReasoning(msg); reasoning != "" && param.OfAssistant != nil {
		param.OfAssistant.SetExtraFields(map[string]any{"reasoning": reasoning})
	}
	return param
}

// ExtractReasoning extracts the model's reasoning trace from a completion.
// OpenRouter returns it as a top-level "reasoning" field on the message,
// which is not part of the OpenAI schema, so openai-go parks it in
// JSON.ExtraFields rather than a typed field. Returns "" for providers/models
// that send no reasoning.
//
// Note [respjson.Field.Valid] is deliberately not consulted: it reports false
// for every extra field (the parser only tracks presence for fields it
// knows), so gating on it would drop all reasoning.
func ExtractReasoning(msg openai.ChatCompletionMessage) string {
	f, ok := msg.JSON.ExtraFields["reasoning"]
	if !ok {
		return ""
	}
	raw := f.Raw()
	if raw == "" || raw == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		// Not a JSON string (some providers nest structured reasoning blocks);
		// the raw form still beats nothing for a debugger.
		return raw
	}
	return s
}
