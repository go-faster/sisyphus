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

// Options configures a Client.
type Options struct {
	// BaseURL overrides the API base URL (useful for tests / self-hosted).
	BaseURL string
}

func (opts *Options) setDefaults() {
	if opts.BaseURL == "" {
		opts.BaseURL = defaultBaseURL
	}
}

// New returns a Client configured for the given API key.
func New(apiKey string, opts Options) *Client {
	opts.setDefaults()
	ropts := []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithBaseURL(opts.BaseURL),
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
