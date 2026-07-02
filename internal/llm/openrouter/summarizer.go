package openrouter

import (
	"context"
	_ "embed"
	"strings"

	"github.com/go-faster/errors"
	"github.com/openai/openai-go/v3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/go-faster/sisyphus/internal/index"
)

//go:embed prompts/summarizer.md
var defaultSummarizerPrompt string

// Summarizer implements index.Summarizer via OpenRouter.
type Summarizer struct {
	client *Client
	model  string
	prompt string
}

// SummarizerOptions configures a Summarizer.
type SummarizerOptions struct {
	// Prompt overrides the default system prompt.
	Prompt string
}

func (opts *SummarizerOptions) setDefaults() {
	if opts.Prompt == "" {
		opts.Prompt = strings.TrimSpace(defaultSummarizerPrompt)
	}
}

// NewSummarizer returns a Summarizer that uses the given model.
func NewSummarizer(client *Client, model string, opts SummarizerOptions) *Summarizer {
	opts.setDefaults()
	return &Summarizer{
		client: client,
		model:  model,
		prompt: opts.Prompt,
	}
}

// Summarize asks the model to produce a concise summary of prompt.
func (s *Summarizer) Summarize(ctx context.Context, prompt string) (string, error) {
	ctx, span := s.client.tracer.Start(ctx, "llm.Summarize",
		trace.WithAttributes(
			attribute.String("model", s.model),
			attribute.Int("prompt.len", len(prompt)),
		),
	)
	defer span.End()

	msgs := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(s.prompt),
		openai.UserMessage(prompt),
	}
	result, err := s.client.complete(ctx, s.model, msgs)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return "", errors.Wrap(err, "summarize")
	}
	span.SetAttributes(attribute.Int("summary.len", len(result)))
	return result, nil
}

var _ index.Summarizer = (*Summarizer)(nil)
