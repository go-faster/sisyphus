package openrouter

import (
	"context"
	"encoding/json"

	"github.com/go-faster/sdk/zctx"
	"github.com/openai/openai-go/v3"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/go-faster/sisyphus/internal/agent"
)

// logCompletion records one raw model turn: the visible content, the reasoning
// trace when the provider returns one, and the tool calls it asked for. Without
// this a finished run leaves nothing behind but token counters and HTTP status
// codes, so a wrong answer cannot be traced back to the reasoning that produced
// it. Logged from the client so every caller is covered — the agent loop
// (/investigate, agentic /context) and the one-shot Answerer alike.
//
// Debug level: the payload is large and quotes untrusted retrieved content
// verbatim.
func logCompletion(ctx context.Context, model string, msg openai.ChatCompletionMessage, usage agent.Usage) {
	lg := zctx.From(ctx)
	if !lg.Core().Enabled(zapcore.DebugLevel) {
		return
	}

	fields := []zap.Field{
		zap.String("model", model),
		zap.Int64("prompt_tokens", usage.PromptTokens),
		zap.Int64("completion_tokens", usage.CompletionTokens),
	}
	if msg.Content != "" {
		fields = append(fields, zap.String("content", msg.Content))
	}
	if r := completionReasoning(msg); r != "" {
		fields = append(fields, zap.String("reasoning", r))
	}
	if len(msg.ToolCalls) > 0 {
		calls := make([]string, 0, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			calls = append(calls, tc.Function.Name)
		}
		fields = append(fields, zap.Strings("tool_calls", calls))
	}
	lg.Debug("llm completion", fields...)
}

// completionReasoning extracts the model's reasoning trace. OpenRouter returns
// it as a top-level "reasoning" field on the message, which is not part of the
// OpenAI schema, so openai-go parks it in JSON.ExtraFields rather than a typed
// field. Returns "" for providers/models that send no reasoning.
//
// Note [respjson.Field.Valid] is deliberately not consulted: it reports false
// for every extra field (the parser only tracks presence for fields it knows),
// so gating on it would drop all reasoning.
func completionReasoning(msg openai.ChatCompletionMessage) string {
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
