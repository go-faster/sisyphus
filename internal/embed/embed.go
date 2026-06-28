// Package embed builds embedding providers from runtime configuration.
package embed

import (
	"strings"

	"github.com/go-faster/errors"

	"github.com/go-faster/scpbot/internal/config"
	"github.com/go-faster/scpbot/internal/embed/ollama"
	openrouterembed "github.com/go-faster/scpbot/internal/embed/openrouter"
	"github.com/go-faster/scpbot/internal/index"
)

// New creates the configured embedding provider.
func New(cfg config.Config) (index.Embedder, error) {
	switch strings.ToLower(cfg.EmbedProvider) {
	case "", "ollama":
		return ollama.New(cfg.OllamaURL, cfg.EmbedModel, ollama.EmbedderOptions{Dim: cfg.EmbedDim}), nil
	case "openrouter":
		if cfg.OpenRouter.APIKey == "" {
			return nil, errors.New("SCPBOT_OPENROUTER_API_KEY is required for openrouter embeddings")
		}
		return openrouterembed.New(cfg.OpenRouter.APIKey, cfg.EmbedModel, openrouterembed.EmbedderOptions{Dim: cfg.EmbedDim}), nil
	default:
		return nil, errors.New("unsupported embedding provider: " + cfg.EmbedProvider)
	}
}
