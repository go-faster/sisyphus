package openrouter

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/go-faster/errors"
	"github.com/openai/openai-go/v3"

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

// AnswererOption configures an Answerer.
type AnswererOption func(*Answerer)

// WithAnswererPrompt overrides the system prompt.
func WithAnswererPrompt(p string) AnswererOption {
	return func(a *Answerer) { a.prompt = p }
}

// NewAnswerer returns an Answerer that uses the given model.
func NewAnswerer(client *Client, model string, opts ...AnswererOption) *Answerer {
	a := &Answerer{
		client: client,
		model:  model,
		prompt: strings.TrimSpace(defaultAnswererPrompt),
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Answer constructs a grounded answer from retrieved context chunks.
func (a *Answerer) Answer(ctx context.Context, question string, results []index.Result) (string, error) {
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
	result, err := complete(ctx, a.client.oc, a.model, msgs)
	if err != nil {
		return "", errors.Wrap(err, "answer")
	}
	return result, nil
}

var _ index.Answerer = (*Answerer)(nil)
