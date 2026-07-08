package openrouter

import (
	"context"
	"time"

	"github.com/go-faster/errors"
	"github.com/openai/openai-go/v3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// CompleteWithTools sends a chat completion request to the LLM with the provided tools
// and returns the raw message, which may contain tool calls or direct content.
func (c *Client) CompleteWithTools(
	ctx context.Context,
	model string,
	messages []openai.ChatCompletionMessageParamUnion,
	tools []openai.ChatCompletionToolUnionParam,
) (_ openai.ChatCompletionMessage, rerr error) {
	start := time.Now()
	var promptTokens, completionTokens int64
	ctx, span := c.tracer.Start(ctx, "llm.complete_with_tools",
		trace.WithAttributes(attribute.String("model", model)),
	)
	defer func() {
		if c.m != nil {
			c.m.record(ctx, "complete_with_tools", model, time.Since(start).Seconds(), promptTokens, completionTokens, rerr)
		}
		if rerr != nil {
			span.RecordError(rerr)
			span.SetStatus(codes.Error, rerr.Error())
		}
		span.End()
	}()

	req := openai.ChatCompletionNewParams{
		Model:    model,
		Messages: messages,
	}
	if len(tools) > 0 {
		req.Tools = tools
	}

	resp, err := c.oc.Chat.Completions.New(ctx, req)
	if err != nil {
		return openai.ChatCompletionMessage{}, errors.Wrap(err, "chat completion with tools")
	}
	if len(resp.Choices) == 0 {
		return openai.ChatCompletionMessage{}, errors.New("openrouter returned no choices")
	}

	promptTokens = resp.Usage.PromptTokens
	completionTokens = resp.Usage.CompletionTokens
	span.SetAttributes(
		attribute.Int64("tokens.prompt", promptTokens),
		attribute.Int64("tokens.completion", completionTokens),
	)

	return resp.Choices[0].Message, nil
}
