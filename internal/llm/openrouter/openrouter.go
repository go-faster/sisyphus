// Package openrouter implements index.Summarizer and index.Answerer backed by
// the OpenRouter chat-completions API via the official openai-go SDK.
package openrouter

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-faster/errors"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/go-faster/scpbot/internal/index"
)

const defaultBaseURL = "https://openrouter.ai/api/v1"

// Client wraps the openai-go SDK client pointed at OpenRouter.
type Client struct {
	oc openai.Client
}

// Option is a functional option for Client.
type Option func(opts *[]option.RequestOption)

// WithBaseURL overrides the API base URL (useful for tests / self-hosted).
func WithBaseURL(u string) Option {
	return func(opts *[]option.RequestOption) {
		*opts = append(*opts, option.WithBaseURL(u))
	}
}

// New returns a Client configured for the given API key and model.
func New(apiKey string, opts ...Option) *Client {
	ropts := []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithBaseURL(defaultBaseURL),
	}
	for _, o := range opts {
		o(&ropts)
	}
	return &Client{oc: openai.NewClient(ropts...)}
}

func complete(ctx context.Context, oc openai.Client, model string, messages []openai.ChatCompletionMessageParamUnion) (string, error) {
	resp, err := oc.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:    model,
		Messages: messages,
	})
	if err != nil {
		return "", errors.Wrap(err, "chat completion")
	}
	if len(resp.Choices) == 0 {
		return "", errors.New("openrouter returned no choices")
	}
	return strings.TrimSpace(resp.Choices[0].Message.Content), nil
}

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
