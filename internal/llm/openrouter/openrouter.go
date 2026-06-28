// Package openrouter implements index.Summarizer and index.Answerer backed by
// the OpenRouter chat-completions API via the official openai-go SDK.
package openrouter

import (
	"context"
	"strings"

	"github.com/go-faster/errors"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
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

// New returns a Client configured for the given API key.
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
