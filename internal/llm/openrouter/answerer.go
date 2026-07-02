package openrouter

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/go-faster/errors"
	"github.com/openai/openai-go/v3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/go-faster/scpbot/internal/index"
)

//go:embed prompts/answerer.md
var defaultAnswererPrompt string

// Answerer implements index.Answerer via OpenRouter.
type Answerer struct {
	client *Client
	model  string
	prompt string
}

// AnswererOptions configures an Answerer.
type AnswererOptions struct {
	// Prompt overrides the default system prompt.
	Prompt string
}

func (opts *AnswererOptions) setDefaults() {
	if opts.Prompt == "" {
		opts.Prompt = strings.TrimSpace(defaultAnswererPrompt)
	}
}

// NewAnswerer returns an Answerer that uses the given model.
func NewAnswerer(client *Client, model string, opts AnswererOptions) *Answerer {
	opts.setDefaults()
	return &Answerer{
		client: client,
		model:  model,
		prompt: opts.Prompt,
	}
}

// Answer constructs a grounded answer from retrieved context chunks.
func (a *Answerer) Answer(ctx context.Context, question string, results []index.Result) (string, error) {
	ctx, span := a.client.tracer.Start(ctx, "llm.Answer",
		trace.WithAttributes(
			attribute.String("model", a.model),
			attribute.Int("results.count", len(results)),
		),
	)
	defer span.End()

	var sb strings.Builder
	for i, r := range results {
		fmt.Fprintf(&sb, "--- Source %d", i+1)
		if r.Chunk.Title != "" {
			fmt.Fprintf(&sb, ": %s", r.Chunk.Title)
		}
		fmt.Fprintf(&sb, " ---\n%s\n\n", r.Chunk.Text)
	}

	msgs := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(a.prompt),
		openai.UserMessage(fmt.Sprintf("Context:\n%s\nQuestion: %s", sb.String(), question)),
	}
	result, err := a.client.complete(ctx, a.model, msgs)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return "", errors.Wrap(err, "answer")
	}
	span.SetAttributes(attribute.Int("answer.len", len(result)))
	return result, nil
}

var _ index.Answerer = (*Answerer)(nil)
