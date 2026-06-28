package openrouter

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-faster/errors"
	"github.com/openai/openai-go/v3"

	"github.com/go-faster/scpbot/internal/index"
)

// Answerer implements index.Answerer via OpenRouter.
type Answerer struct {
	client *Client
	model  string
}

// NewAnswerer returns an Answerer that uses the given model.
func NewAnswerer(client *Client, model string) *Answerer {
	return &Answerer{client: client, model: model}
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
		openai.SystemMessage("You are a helpful internal support assistant. Answer based only on the provided context. If the context does not contain enough information, say so explicitly. Be concise and precise."),
		openai.UserMessage(fmt.Sprintf("Context:\n%s\nQuestion: %s", sb.String(), question)),
	}
	result, err := complete(ctx, a.client.oc, a.model, msgs)
	if err != nil {
		return "", errors.Wrap(err, "answer")
	}
	return result, nil
}

var _ index.Answerer = (*Answerer)(nil)
