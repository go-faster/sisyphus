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
	"github.com/go-faster/sdk/zctx"
	_ "github.com/jackc/pgx/v5/stdlib"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/go-faster/scpbot/internal/config"
	"github.com/go-faster/scpbot/internal/embed"
	"github.com/go-faster/scpbot/internal/ent"
	"github.com/go-faster/scpbot/internal/index"
	"github.com/go-faster/scpbot/internal/llm/openrouter"
	"github.com/go-faster/scpbot/internal/llm/stub"
	"github.com/go-faster/scpbot/internal/netclient"
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

// NewOptions configures wiring.
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

// New builds Postgres, FTS, embedder, optional Qdrant vector store, retrieval
// service and answerer (OpenRouter or stub) exactly as the original main did.
// It performs schema and FTS migrations. On error, resources are cleaned up.
func New(ctx context.Context, cfg config.Config, opts NewOptions) (Components, error) {
	opts.setDefaults()

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
	embedder, err := embed.New(ctx, cfg, embed.NewOptions{
		TracerProvider: opts.TracerProvider,
		MeterProvider:  opts.MeterProvider,
	})
	if err != nil {
		_ = db.Close()
		return Components{}, errors.Wrap(err, "embedder")
	}

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
		zctx.From(ctx).Warn("qdrant unavailable, vector search disabled", zap.Error(err))
	} else if err := store.EnsureCollection(ctx); err != nil {
		zctx.From(ctx).Warn("qdrant collection setup failed, vector search disabled", zap.Error(err))
	} else {
		vector = store
	}

	retr, err := retrieval.New(pg, vector)
	if err != nil {
		_ = db.Close()
		return Components{}, errors.Wrap(err, "retrieval")
	}

	var answerer index.Answerer
	if cfg.OpenRouter.Enabled() {
		zctx.From(ctx).Info("openrouter LLM enabled", zap.String("model", cfg.OpenRouter.Model))
		httpClient, err := netclient.HTTPClient(ctx, "openrouter", cfg.Proxies.OpenRouter, netclient.HTTPClientOptions{
			TracerProvider: opts.TracerProvider,
			MeterProvider:  opts.MeterProvider,
		})
		if err != nil {
			_ = db.Close()
			return Components{}, errors.Wrap(err, "openrouter http client")
		}
		orClient := openrouter.New(cfg.OpenRouter.APIKey, openrouter.Options{HTTPClient: httpClient})
		answerer = openrouter.NewAnswerer(orClient, cfg.OpenRouter.Model, openrouter.AnswererOptions{})
	} else {
		zctx.From(ctx).Warn("openrouter not configured, using stub answerer")
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
