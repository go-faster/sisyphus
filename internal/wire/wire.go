// Package wire provides shared construction of core services (retrieval, answerer)
// used by cmd/scpd, cmd/scpmcp, and tests. Extracted from cmd/scpbot/main.go.
package wire

import (
	"context"
	"database/sql"
	"net"
	"strconv"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/go-faster/errors"
	"go.uber.org/zap"

	"github.com/go-faster/scpbot/internal/config"
	"github.com/go-faster/scpbot/internal/embed/ollama"
	"github.com/go-faster/scpbot/internal/ent"
	"github.com/go-faster/scpbot/internal/index"
	"github.com/go-faster/scpbot/internal/llm/openrouter"
	"github.com/go-faster/scpbot/internal/llm/stub"
	"github.com/go-faster/scpbot/internal/retrieval"
	pgsearch "github.com/go-faster/scpbot/internal/search/postgres"
	"github.com/go-faster/scpbot/internal/search/qdrant"
)

// Retriever is the minimal retrieval interface used by API handlers, bot,
// and mcpserver. Matches the method on *retrieval.Service.
type Retriever interface {
	Retrieve(ctx context.Context, q index.Query) ([]index.Result, error)
}

// Components holds the wired services for retrieval and answering.
type Components struct {
	Retriever Retriever
	Answerer  index.Answerer

	closeDB func()
}

// Close releases resources held by the components.
func (c Components) Close() {
	if c.closeDB != nil {
		c.closeDB()
	}
}

// New builds Postgres, FTS, embedder, optional Qdrant vector store, retrieval
// service and answerer (OpenRouter or stub) exactly as the original main did.
// It performs schema and FTS migrations. On error, resources are cleaned up.
func New(ctx context.Context, lg *zap.Logger, cfg config.Config) (Components, error) {
	db, err := sql.Open("pgx", cfg.DatabaseDSN)
	if err != nil {
		return Components{}, errors.Wrap(err, "open db")
	}
	cleanup := func() { _ = db.Close() }

	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	if err := client.Schema.Create(ctx); err != nil {
		_ = db.Close()
		return Components{}, errors.Wrap(err, "migrate schema")
	}

	pg := pgsearch.New(db)
	if err := pg.Migrate(ctx); err != nil {
		_ = db.Close()
		return Components{}, errors.Wrap(err, "migrate fts")
	}

	// Embedder + vector store.
	embedder := ollama.New(cfg.OllamaURL, cfg.EmbedModel, ollama.EmbedderOptions{Dim: cfg.EmbedDim})

	var vector index.Searcher
	host, port, err := splitHostPort(cfg.QdrantAddr)
	if err != nil {
		_ = db.Close()
		return Components{}, errors.Wrap(err, "qdrant addr")
	}
	store, err := qdrant.New(qdrant.Config{
		Host:       host,
		Port:       port,
		Collection: cfg.QdrantCollection,
		Dim:        cfg.EmbedDim,
		Embedder:   embedder,
	})
	if err != nil {
		lg.Warn("qdrant unavailable, vector search disabled", zap.Error(err))
	} else if err := store.EnsureCollection(ctx); err != nil {
		lg.Warn("qdrant collection setup failed, vector search disabled", zap.Error(err))
	} else {
		vector = store
	}

	retr, err := retrieval.New(pg, vector, lg)
	if err != nil {
		_ = db.Close()
		return Components{}, errors.Wrap(err, "retrieval")
	}

	var answerer index.Answerer
	if cfg.OpenRouter.Enabled() {
		lg.Info("openrouter LLM enabled", zap.String("model", cfg.OpenRouter.Model))
		orClient := openrouter.New(cfg.OpenRouter.APIKey, openrouter.Options{})
		answerer = openrouter.NewAnswerer(orClient, cfg.OpenRouter.Model, openrouter.AnswererOptions{})
	} else {
		lg.Warn("openrouter not configured, using stub answerer")
		answerer = stub.NewAnswerer()
	}

	return Components{
		Retriever: retr,
		Answerer:  answerer,
		closeDB:   cleanup,
	}, nil
}

func splitHostPort(addr string) (host string, port int, err error) {
	h, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, errors.Wrap(err, "split host port")
	}
	p, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, errors.Wrap(err, "parse port")
	}
	return h, p, nil
}
