// Package config loads scpbot configuration from the environment.
package config

import (
	"os"
	"strconv"

	"github.com/go-faster/errors"
)

// Config holds all runtime configuration, sourced from SCPBOT_* env vars.
type Config struct {
	HTTPAddr    string // SCPBOT_HTTP_ADDR
	DatabaseDSN string // SCPBOT_DATABASE_DSN

	QdrantAddr       string // SCPBOT_QDRANT_ADDR (host:port, gRPC)
	QdrantCollection string // SCPBOT_QDRANT_COLLECTION

	OllamaURL  string // SCPBOT_OLLAMA_URL
	EmbedModel string // SCPBOT_EMBED_MODEL
	EmbedDim   int    // SCPBOT_EMBED_DIM

	CatalogPath string // SCPBOT_CATALOG_PATH

	OpenRouter OpenRouter
	Telegram   Telegram
}

// OpenRouter holds configuration for the OpenRouter LLM API.
type OpenRouter struct {
	APIKey string // SCPBOT_OPENROUTER_API_KEY
	Model  string // SCPBOT_OPENROUTER_MODEL
}

// Enabled reports whether OpenRouter is configured.
func (o OpenRouter) Enabled() bool { return o.APIKey != "" }

// Telegram holds gotd auth configuration (plan: user session + bot).
type Telegram struct {
	AppID      int    // SCPBOT_TELEGRAM_APP_ID
	AppHash    string // SCPBOT_TELEGRAM_APP_HASH
	BotToken   string // SCPBOT_TELEGRAM_BOT_TOKEN
	SessionDir string // SCPBOT_TELEGRAM_SESSION_DIR
}

// Load reads configuration from the environment, applying defaults.
func Load() (Config, error) {
	c := Config{
		HTTPAddr:         env("SCPBOT_HTTP_ADDR", ":8080"),
		DatabaseDSN:      os.Getenv("SCPBOT_DATABASE_DSN"),
		QdrantAddr:       env("SCPBOT_QDRANT_ADDR", "localhost:6334"),
		QdrantCollection: env("SCPBOT_QDRANT_COLLECTION", "corp_chunks"),
		OllamaURL:        env("SCPBOT_OLLAMA_URL", "http://localhost:11434"),
		EmbedModel:       env("SCPBOT_EMBED_MODEL", "bge-m3"),
		EmbedDim:         envInt("SCPBOT_EMBED_DIM", 1024),
		CatalogPath:      env("SCPBOT_CATALOG_PATH", "service_catalog.yaml"),
		OpenRouter: OpenRouter{
			APIKey: os.Getenv("SCPBOT_OPENROUTER_API_KEY"),
			Model:  env("SCPBOT_OPENROUTER_MODEL", "openai/gpt-4o-mini"),
		},
		Telegram: Telegram{
			AppID:      envInt("SCPBOT_TELEGRAM_APP_ID", 0),
			AppHash:    os.Getenv("SCPBOT_TELEGRAM_APP_HASH"),
			BotToken:   os.Getenv("SCPBOT_TELEGRAM_BOT_TOKEN"),
			SessionDir: env("SCPBOT_TELEGRAM_SESSION_DIR", "./session"),
		},
	}
	if c.DatabaseDSN == "" {
		return Config{}, errors.New("SCPBOT_DATABASE_DSN is required")
	}
	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
