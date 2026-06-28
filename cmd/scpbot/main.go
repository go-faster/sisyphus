// Command scpbot runs the ingestion/index API and the Telegram /context bot.
package main

import (
	"context"
	"database/sql"
	"net"
	"net/http"
	"strconv"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/app"
	_ "github.com/jackc/pgx/v5/stdlib"
	"go.uber.org/zap"

	"github.com/go-faster/scpbot/internal/api"
	"github.com/go-faster/scpbot/internal/bot"
	"github.com/go-faster/scpbot/internal/config"
	"github.com/go-faster/scpbot/internal/embed"
	"github.com/go-faster/scpbot/internal/ent"
	"github.com/go-faster/scpbot/internal/index"
	"github.com/go-faster/scpbot/internal/llm/openrouter"
	"github.com/go-faster/scpbot/internal/llm/stub"
	"github.com/go-faster/scpbot/internal/oas"
	"github.com/go-faster/scpbot/internal/retrieval"
	pgsearch "github.com/go-faster/scpbot/internal/search/postgres"
	"github.com/go-faster/scpbot/internal/search/qdrant"
)

func main() {
	app.Run(func(ctx context.Context, lg *zap.Logger, _ *app.Telemetry) error {
		cfg, err := config.Load()
		if err != nil {
			return errors.Wrap(err, "config")
		}
		return run(ctx, lg, cfg)
	})
}

func run(ctx context.Context, lg *zap.Logger, cfg config.Config) error {
	// Postgres via pgx stdlib -> ent.
	db, err := sql.Open("pgx", cfg.DatabaseDSN)
	if err != nil {
		return errors.Wrap(err, "open db")
	}
	defer func() { _ = db.Close() }()

	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	if err := client.Schema.Create(ctx); err != nil {
		return errors.Wrap(err, "migrate schema")
	}

	pg := pgsearch.New(db)
	if err := pg.Migrate(ctx); err != nil {
		return errors.Wrap(err, "migrate fts")
	}

	// Embedder + vector store.
	embedder, err := embed.New(cfg)
	if err != nil {
		return errors.Wrap(err, "embedder")
	}

	var vector index.Searcher
	host, port, err := splitHostPort(cfg.QdrantAddr)
	if err != nil {
		return errors.Wrap(err, "qdrant addr")
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
		return errors.Wrap(err, "retrieval")
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

	// HTTP API.
	handler := api.New(retr, answerer, "0.1.0")
	srv, err := oas.NewServer(handler)
	if err != nil {
		return errors.Wrap(err, "oas server")
	}
	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 2)
	go func() {
		lg.Info("http listening", zap.String("addr", cfg.HTTPAddr))
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- errors.Wrap(err, "http serve")
		}
	}()

	// Telegram bot (optional: only when credentials are present).
	if cfg.Telegram.AppID != 0 && cfg.Telegram.BotToken != "" {
		b := bot.New(bot.Config{
			AppID:      cfg.Telegram.AppID,
			AppHash:    cfg.Telegram.AppHash,
			BotToken:   cfg.Telegram.BotToken,
			SessionDir: cfg.Telegram.SessionDir,
		}, retr, answerer, lg.Named("bot"))
		go func() {
			if err := b.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				errc <- errors.Wrap(err, "bot")
			}
		}()
	} else {
		lg.Warn("telegram credentials missing, bot disabled")
	}

	select {
	case <-ctx.Done():
	case err := <-errc:
		_ = shutdown(httpSrv)
		return err
	}
	return shutdown(httpSrv)
}

func shutdown(srv *http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
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
