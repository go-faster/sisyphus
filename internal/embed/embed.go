// Package embed builds embedding providers from runtime configuration.
package embed

import (
	"strings"

	"github.com/go-faster/errors"

	"github.com/go-faster/scpbot/internal/config"
	"github.com/go-faster/scpbot/internal/embed/ollama"
	openrouterembed "github.com/go-faster/scpbot/internal/embed/openrouter"
	"github.com/go-faster/scpbot/internal/index"
	"github.com/go-faster/scpbot/internal/netclient"
)

// New creates the configured embedding provider.
func New(cfg config.Config) (index.Embedder, error) {
	switch strings.ToLower(cfg.EmbedProvider) {
	case "", "ollama":
		httpClient, err := netclient.HTTPClient(cfg.Proxies.Ollama)
		if err != nil {
			return nil, errors.Wrap(err, "ollama http client")
		}
		return ollama.New(cfg.OllamaURL, cfg.EmbedModel, ollama.EmbedderOptions{
			Dim:        cfg.EmbedDim,
			HTTPClient: httpClient,
		}), nil
	case "openrouter":
		if cfg.OpenRouter.APIKey == "" {
			return nil, errors.New("openrouter api_key is required for openrouter embeddings")
		}
		httpClient, err := netclient.HTTPClient(cfg.Proxies.OpenRouter)
		if err != nil {
			return nil, errors.Wrap(err, "openrouter http client")
		}
		return openrouterembed.New(cfg.OpenRouter.APIKey, cfg.EmbedModel, openrouterembed.EmbedderOptions{
			Dim:        cfg.EmbedDim,
			HTTPClient: httpClient,
		}), nil
	default:
		return nil, errors.New("unsupported embedding provider: " + cfg.EmbedProvider)
	}
}
