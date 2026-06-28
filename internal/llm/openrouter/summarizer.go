package openrouter

import (
	"context"

	"github.com/go-faster/errors"
	"github.com/openai/openai-go/v3"

	"github.com/go-faster/scpbot/internal/index"
)

// Summarizer implements index.Summarizer via OpenRouter.
type Summarizer struct {
	client *Client
	model  string
}

// NewSummarizer returns a Summarizer that uses the given model.
func NewSummarizer(client *Client, model string) *Summarizer {
	return &Summarizer{client: client, model: model}
}

// Summarize asks the model to produce a concise summary of prompt.
func (s *Summarizer) Summarize(ctx context.Context, prompt string) (string, error) {
	msgs := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage("You are a concise technical summarizer. Return a single short paragraph summary with no extra commentary."),
		openai.UserMessage(prompt),
	}
	result, err := complete(ctx, s.client.oc, s.model, msgs)
	if err != nil {
		return "", errors.Wrap(err, "summarize")
	}
	return result, nil
}

var _ index.Summarizer = (*Summarizer)(nil)
