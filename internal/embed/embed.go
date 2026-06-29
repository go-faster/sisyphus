// Package embed builds embedding providers from runtime configuration.
package embed

import (
	"strings"

	"github.com/go-faster/errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/go-faster/scpbot/internal/config"
	"github.com/go-faster/scpbot/internal/embed/ollama"
	openrouterembed "github.com/go-faster/scpbot/internal/embed/openrouter"
	"github.com/go-faster/scpbot/internal/index"
	"github.com/go-faster/scpbot/internal/netclient"
)

// NewOptions configures the embedding provider factory.
type NewOptions struct {
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
}

func (opts *NewOptions) setDefaults() {
	if opts.TracerProvider == nil {
		opts.TracerProvider = otel.GetTracerProvider()
	}
	if opts.MeterProvider == nil {
		opts.MeterProvider = otel.GetMeterProvider()
	}
}

// New creates the configured embedding provider.
func New(cfg config.Config, opts NewOptions) (index.Embedder, error) {
	opts.setDefaults()

	httpOpts := netclient.HTTPClientOptions{
		TracerProvider: opts.TracerProvider,
		MeterProvider:  opts.MeterProvider,
	}

	switch strings.ToLower(cfg.EmbedProvider) {
	case "", "ollama":
		httpClient, err := netclient.HTTPClient(cfg.Proxies.Ollama, httpOpts)
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
		httpClient, err := netclient.HTTPClient(cfg.Proxies.OpenRouter, httpOpts)
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
