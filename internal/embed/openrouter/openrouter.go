// Package openrouter implements index.Embedder using OpenRouter's OpenAI-compatible embeddings API.
package openrouter

import (
	"context"
	"net/http"

	"github.com/go-faster/errors"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/go-faster/scpbot/internal/index"
)

const defaultBaseURL = "https://openrouter.ai/api/v1"

// Embedder is an index.Embedder backed by OpenRouter's OpenAI-compatible API.
type Embedder struct {
	client openai.Client
	model  string
	dim    int
}

// EmbedderOptions configures an Embedder.
type EmbedderOptions struct {
	// BaseURL overrides the OpenRouter API base URL (useful for tests / self-hosted).
	BaseURL string
	// HTTPClient sets the HTTP client used for requests.
	HTTPClient *http.Client
	// Dim sets the dimension of the embeddings.
	// Defaults to 1024.
	Dim int
}

func (opts *EmbedderOptions) setDefaults() {
	if opts.BaseURL == "" {
		opts.BaseURL = defaultBaseURL
	}
	if opts.Dim == 0 {
		opts.Dim = 1024
	}
}

// New creates a new OpenRouter embedder.
func New(apiKey, model string, opts EmbedderOptions) *Embedder {
	opts.setDefaults()
	ropts := []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithBaseURL(opts.BaseURL),
	}
	if opts.HTTPClient != nil {
		opts := option.WithHTTPClient(opts.HTTPClient)
		ropts = append(ropts, opts)
	}
	client := openai.NewClient(ropts...)
	return &Embedder{
		client: client,
		model:  model,
		dim:    opts.Dim,
	}
}

// Embed produces embedding vectors for the given texts.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	resp, err := e.client.Embeddings.New(ctx, openai.EmbeddingNewParams{
		Input: openai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: texts},
		Model: e.model,
	})
	if err != nil {
		return nil, errors.Wrap(err, "create embedding")
	}
	if len(resp.Data) != len(texts) {
		return nil, errors.Wrapf(nil, "openrouter returned %d embeddings but %d were requested", len(resp.Data), len(texts))
	}

	out := make([][]float32, len(texts))
	for _, item := range resp.Data {
		idx := int(item.Index)
		if idx < 0 || idx >= len(texts) {
			return nil, errors.Wrapf(nil, "openrouter returned embedding with invalid index %d", item.Index)
		}
		if out[idx] != nil {
			return nil, errors.Wrapf(nil, "openrouter returned duplicate embedding index %d", item.Index)
		}

		vec := make([]float32, len(item.Embedding))
		for i, v := range item.Embedding {
			vec[i] = float32(v)
		}
		out[idx] = vec
	}

	for i, vec := range out {
		if vec == nil {
			return nil, errors.Wrapf(nil, "openrouter did not return embedding index %d", i)
		}
	}
	return out, nil
}

// Dim returns the dimension of the embeddings.
func (e *Embedder) Dim() int {
	return e.dim
}

var _ index.Embedder = (*Embedder)(nil)
