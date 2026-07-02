package embed

import (
	"context"
	"testing"

	"github.com/go-faster/sisyphus/internal/config"
	"github.com/go-faster/sisyphus/internal/embed/ollama"
	openrouterembed "github.com/go-faster/sisyphus/internal/embed/openrouter"
)

func TestNew_DefaultOllama(t *testing.T) {
	t.Parallel()

	got, err := New(context.Background(), config.Config{
		OllamaURL:  "http://localhost:11434",
		EmbedModel: "bge-m3",
		EmbedDim:   1024,
	}, NewOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := got.(*ollama.Embedder); !ok {
		t.Fatalf("got %T, want *ollama.Embedder", got)
	}
}

func TestNew_OpenRouter(t *testing.T) {
	t.Parallel()

	got, err := New(context.Background(), config.Config{
		EmbedProvider: "openrouter",
		EmbedModel:    "test-embed",
		EmbedDim:      1536,
		OpenRouter: config.OpenRouter{
			APIKey: "test-key",
		},
	}, NewOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := got.(*openrouterembed.Embedder); !ok {
		t.Fatalf("got %T, want *openrouter.Embedder", got)
	}
}

func TestNew_OpenRouterRequiresAPIKey(t *testing.T) {
	t.Parallel()

	_, err := New(context.Background(), config.Config{EmbedProvider: "openrouter"}, NewOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestNew_UnsupportedProvider(t *testing.T) {
	t.Parallel()

	_, err := New(context.Background(), config.Config{EmbedProvider: "unknown"}, NewOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
}
