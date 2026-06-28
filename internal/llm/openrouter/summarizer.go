package openrouter

import (
	"context"
	_ "embed"
	"strings"

	"github.com/go-faster/errors"
	"github.com/openai/openai-go/v3"

	"github.com/go-faster/scpbot/internal/index"
)

//go:embed prompts/summarizer.md
var defaultSummarizerPrompt string

// Summarizer implements index.Summarizer via OpenRouter.
type Summarizer struct {
	client *Client
	model  string
	prompt string
}

// SummarizerOption configures a Summarizer.
type SummarizerOption func(*Summarizer)

// WithSummarizerPrompt overrides the system prompt.
func WithSummarizerPrompt(p string) SummarizerOption {
	return func(s *Summarizer) { s.prompt = p }
}

// NewSummarizer returns a Summarizer that uses the given model.
func NewSummarizer(client *Client, model string, opts ...SummarizerOption) *Summarizer {
	s := &Summarizer{
		client: client,
		model:  model,
		prompt: strings.TrimSpace(defaultSummarizerPrompt),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Summarize asks the model to produce a concise summary of prompt.
func (s *Summarizer) Summarize(ctx context.Context, prompt string) (string, error) {
	msgs := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(s.prompt),
		openai.UserMessage(prompt),
	}
	result, err := complete(ctx, s.client.oc, s.model, msgs)
	if err != nil {
		return "", errors.Wrap(err, "summarize")
	}
	return result, nil
}

var _ index.Summarizer = (*Summarizer)(nil)
