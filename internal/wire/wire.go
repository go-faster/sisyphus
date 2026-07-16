// Package wire constructs shared core services: db, embedder, vector store, retrieval, answerer.
package wire

import (
	"context"
	"database/sql"
	"net"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/XSAM/otelsql"
	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/agent"
	"github.com/go-faster/sisyphus/internal/answer"
	"github.com/go-faster/sisyphus/internal/cliversion"
	"github.com/go-faster/sisyphus/internal/config"
	"github.com/go-faster/sisyphus/internal/content"
	"github.com/go-faster/sisyphus/internal/embed"
	"github.com/go-faster/sisyphus/internal/ent"
	entmigrate "github.com/go-faster/sisyphus/internal/ent/migrate"
	"github.com/go-faster/sisyphus/internal/fetch"
	"github.com/go-faster/sisyphus/internal/index"
	ingestgit "github.com/go-faster/sisyphus/internal/ingest/git"
	"github.com/go-faster/sisyphus/internal/llm/openrouter"
	"github.com/go-faster/sisyphus/internal/llm/stub"
	"github.com/go-faster/sisyphus/internal/mcpclient"
	"github.com/go-faster/sisyphus/internal/netclient"
	"github.com/go-faster/sisyphus/internal/pipeline"
	"github.com/go-faster/sisyphus/internal/retrieval"
	pgsearch "github.com/go-faster/sisyphus/internal/search/postgres"
	"github.com/go-faster/sisyphus/internal/search/qdrant"

	_ "github.com/jackc/pgx/v5/stdlib" // register pgx driver
)

// Retriever is the minimal retrieval interface used by API handlers, bot,
// and mcpserver. Matches the method on *retrieval.Service.
type Retriever interface {
	Retrieve(ctx context.Context, q index.Query) ([]index.Result, error)
}

// Services holds low-level shared infrastructure: database, search, embedder, vectors.
type Services struct {
	DB           *ent.Client
	SQLDB        *sql.DB
	PG           *pgsearch.Searcher
	Embedder     index.Embedder
	Searcher     index.Searcher       // for retrieval (nil if qdrant unavailable)
	Vectors      pipeline.VectorStore // for indexing (nil if qdrant unavailable)
	VectorHealth HealthChecker

	closeDB func()
}

// Close releases resources held by Services.
func (s *Services) Close() {
	if s.closeDB != nil {
		s.closeDB()
	}
}

// Components holds the wired services for retrieval and answering.
type Components struct {
	DB       *ent.Client
	Embedder index.Embedder
	Vectors  pipeline.VectorStore

	Retriever       Retriever
	Answerer        index.Answerer
	ContentResolver index.ContentResolver
	URLFetcher      index.URLFetcher
	Health          HealthChecker

	closeDB func()
}

// HealthChecker verifies dependencies required to serve traffic.
type HealthChecker interface {
	CheckHealth(ctx context.Context) error
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
	UserAgent      string

	// RunMigrations applies pending schema migrations on startup. Only one
	// long-running service should set this — sisyphus — so migrations run
	// exactly once per deploy instead of racing across every process/replica
	// sharing the database.
	RunMigrations bool
}

func (opts *NewOptions) setDefaults() {
	if opts.TracerProvider == nil {
		opts.TracerProvider = otel.GetTracerProvider()
	}
	if opts.MeterProvider == nil {
		opts.MeterProvider = otel.GetMeterProvider()
	}
	if opts.UserAgent == "" {
		if info, ok := cliversion.GetInfo("github.com/go-faster/sisyphus"); ok {
			opts.UserAgent = info.UserAgent("ssapi")
		}
	}
}

// NewServices opens the database, optionally runs migrations, and wires the
// embedder and optional vector store. runMigrations is false for ssingest:
// it runs frequently (cron, concurrently per source) and must not race
// schema migrations against itself or the long-running services.
func NewServices(ctx context.Context, cfg config.Config, lg *zap.Logger, tp trace.TracerProvider, mp metric.MeterProvider, userAgent string, runMigrations bool) (*Services, error) {
	db, err := otelsql.Open("pgx", cfg.DatabaseDSN,
		otelsql.WithAttributes(semconv.DBSystemPostgreSQL),
		otelsql.WithSpanOptions(otelsql.SpanOptions{DisableErrSkip: true}),
	)
	if err != nil {
		return nil, errors.Wrap(err, "open db")
	}
	cleanup := func() { _ = db.Close() }

	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))

	if runMigrations {
		migrator := entmigrate.NewRunner(db)
		if err := migrator.Run(ctx); err != nil {
			cleanup()
			return nil, errors.Wrap(err, "migrate schema")
		}
	}

	pg := pgsearch.New(db, client)

	embedder, err := embed.New(ctx, cfg, embed.NewOptions{
		TracerProvider: tp,
		MeterProvider:  mp,
		UserAgent:      userAgent,
	})
	if err != nil {
		cleanup()
		return nil, errors.Wrap(err, "embedder")
	}

	var (
		searcher     index.Searcher
		vectors      pipeline.VectorStore
		vectorHealth HealthChecker
	)
	host, port, err := splitHostPort(cfg.QdrantAddr)
	if err != nil {
		cleanup()
		return nil, errors.Wrap(err, "qdrant addr")
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
		searcher = store
		vectors = store
		vectorHealth = store
	}

	return &Services{
		DB:           client,
		SQLDB:        db,
		PG:           pg,
		Embedder:     embedder,
		Searcher:     searcher,
		Vectors:      vectors,
		VectorHealth: vectorHealth,
		closeDB:      cleanup,
	}, nil
}

// New builds Postgres, FTS, embedder, optional Qdrant vector store, retrieval
// service and answerer (OpenRouter or stub) exactly as the original main did.
// It performs schema and FTS migrations. On error, resources are cleaned up.
func New(ctx context.Context, cfg config.Config, opts NewOptions) (Components, error) {
	opts.setDefaults()
	lg := zctx.From(ctx)
	var gatewayCleanup func()
	cleanup := func() {
		if gatewayCleanup != nil {
			gatewayCleanup()
		}
	}

	svcs, err := NewServices(ctx, cfg, lg, opts.TracerProvider, opts.MeterProvider, opts.UserAgent, opts.RunMigrations)
	if err != nil {
		return Components{}, err
	}

	retr, err := retrieval.New(svcs.PG, svcs.Searcher, svcs.PG, retrieval.ServiceOptions{
		TracerProvider: opts.TracerProvider,
		MeterProvider:  opts.MeterProvider,
	})
	if err != nil {
		cleanup()
		svcs.Close()
		return Components{}, errors.Wrap(err, "retrieval")
	}

	var (
		answerer index.Answerer
		orClient *openrouter.Client
	)
	if cfg.OpenRouter.Enabled() {
		lg.Info("openrouter LLM enabled", zap.String("model", cfg.OpenRouter.Model))
		httpClient, err := netclient.HTTPClient(ctx, "openrouter", cfg.Proxies.OpenRouter, netclient.HTTPClientOptions{
			TracerProvider: opts.TracerProvider,
			MeterProvider:  opts.MeterProvider,
			UserAgent:      opts.UserAgent,
		})
		if err != nil {
			cleanup()
			svcs.Close()
			return Components{}, errors.Wrap(err, "openrouter http client")
		}
		orClient = openrouter.New(cfg.OpenRouter.APIKey, openrouter.Options{
			HTTPClient:     httpClient,
			TracerProvider: opts.TracerProvider,
			MeterProvider:  opts.MeterProvider,
		})
		answerer = openrouter.NewAnswerer(orClient, cfg.OpenRouter.Model, openrouter.AnswererOptions{})
	} else {
		lg.Warn("openrouter not configured, using stub answerer")
		answerer = stub.NewAnswerer()
	}

	repoMap := make(content.RepoResolverMap)
	for _, src := range cfg.Git.Repos {
		name := src.Repo
		if name == "" {
			name = ingestgit.DefaultRepoName(ingestgit.Source{
				Root: src.Root,
				URL:  src.URL,
			})
		}
		root := src.Root
		if root == "" && src.URL != "" && cfg.Git.WorkDir != "" {
			root = filepath.Join(cfg.Git.WorkDir, ingestgit.SafeDirName(name))
		}
		if name != "" && root != "" {
			repoMap[name] = root
		}
	}

	contentOpts := content.Options{
		Logger:         lg,
		TracerProvider: opts.TracerProvider,
		MeterProvider:  opts.MeterProvider,
	}
	localReader := content.NewLocalRepoReader(repoMap, contentOpts)
	dbReader := content.NewDatabaseReader(svcs.DB, contentOpts)
	contentResolver := content.NewChainResolver([]index.ContentResolver{localReader, dbReader}, contentOpts)
	urlFetcher, err := fetch.New(ctx, cfg.Fetch, cfg.Proxies, fetch.Options{
		Logger:         lg,
		TracerProvider: opts.TracerProvider,
		MeterProvider:  opts.MeterProvider,
		UserAgent:      opts.UserAgent,
	})
	if err != nil {
		cleanup()
		svcs.Close()
		return Components{}, errors.Wrap(err, "url fetcher")
	}

	if cfg.OpenRouter.Enabled() && cfg.Context.Agentic {
		knowledgeTools := answer.NewKnowledgeToolSource(retr, urlFetcher, contentResolver, opts.TracerProvider.Tracer("github.com/go-faster/sisyphus/internal/answer/tools"))
		var gatewayTools agent.ToolSource
		if cfg.Context.GatewayURL != "" {
			gatewayHTTPClient, err := netclient.HTTPClient(ctx, "mcp-gateway", "", netclient.HTTPClientOptions{
				TracerProvider: opts.TracerProvider,
				MeterProvider:  opts.MeterProvider,
				UserAgent:      opts.UserAgent,
			})
			if err != nil {
				cleanup()
				svcs.Close()
				return Components{}, errors.Wrap(err, "mcp gateway http client")
			}
			connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			gatewayClient, err := mcpclient.New(connectCtx, mcpclient.Options{
				URL:           cfg.Context.GatewayURL,
				Headers:       cfg.Context.GatewayHeaders,
				HTTPClient:    gatewayHTTPClient,
				MeterProvider: opts.MeterProvider,
			})
			cancel()
			if err != nil {
				lg.Warn("mcp gateway unavailable, gateway tools disabled", zap.Error(err))
			} else {
				gatewayTools = gatewayClient
				gatewayCleanup = func() { _ = gatewayClient.Close() }
			}
		}
		toolSource := answer.NewMultiToolSource(knowledgeTools, gatewayTools)
		answerer = answer.NewAgenticAnswerer(orClient, toolSource, cfg.OpenRouter.Model, answer.AgenticOptions{
			Logger:         lg,
			Retriever:      retr,
			QueryLimit:     cfg.Context.PreSearchLimit,
			PreSearch:      cfg.Context.PreSearch,
			MaxIterations:  cfg.Context.MaxIterations,
			TimeoutSeconds: cfg.Context.TimeoutSeconds,
			MaxAnswerChars: cfg.Context.MaxAnswerChars,
			SandboxMachine: cfg.Context.SandboxMachine,
			SandboxEnabled: gatewayTools != nil,
			Tracer:         opts.TracerProvider.Tracer("github.com/go-faster/sisyphus/internal/answer"),
			ShowDebugInfo:  cfg.Context.ShowDebugInfo,
		})
	}

	closeDB := svcs.closeDB
	if gatewayCleanup != nil {
		closeDB = func() {
			gatewayCleanup()
			svcs.closeDB()
		}
	}

	return Components{
		DB:              svcs.DB,
		Embedder:        svcs.Embedder,
		Vectors:         svcs.Vectors,
		Retriever:       retr,
		Answerer:        answerer,
		ContentResolver: contentResolver,
		URLFetcher:      urlFetcher,
		Health:          &healthChecker{db: svcs.SQLDB, vectors: svcs.VectorHealth},
		closeDB:         closeDB,
	}, nil
}

type healthChecker struct {
	db        *sql.DB
	vectors   HealthChecker
	mu        sync.Mutex
	checkedAt time.Time
	err       error
}

func (h *healthChecker) CheckHealth(ctx context.Context) error {
	h.mu.Lock()
	if time.Since(h.checkedAt) < 5*time.Second {
		err := h.err
		h.mu.Unlock()
		return err
	}
	h.mu.Unlock()

	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	err := h.check(checkCtx)
	h.mu.Lock()
	h.checkedAt = time.Now()
	h.err = err
	h.mu.Unlock()
	return err
}

func (h *healthChecker) check(ctx context.Context) error {
	if h.db != nil {
		if err := h.db.PingContext(ctx); err != nil {
			return errors.Wrap(err, "postgres")
		}
	}
	if h.vectors != nil {
		if err := h.vectors.CheckHealth(ctx); err != nil {
			return errors.Wrap(err, "qdrant")
		}
	}
	return nil
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
