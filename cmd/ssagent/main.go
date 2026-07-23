package main

import (
	"context"
	"net/http"
	"os"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/XSAM/otelsql"
	"github.com/go-faster/errors"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"github.com/go-faster/sdk/app"
	"github.com/go-faster/sdk/zctx"
	"github.com/spf13/cobra"

	"github.com/go-faster/sisyphus/internal/agent"
	"github.com/go-faster/sisyphus/internal/agentstore"
	"github.com/go-faster/sisyphus/internal/cliversion"
	"github.com/go-faster/sisyphus/internal/cmdutil"
	"github.com/go-faster/sisyphus/internal/config"
	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/httpmw"
	"github.com/go-faster/sisyphus/internal/llm/openrouter"
	"github.com/go-faster/sisyphus/internal/mcpclient"
	"github.com/go-faster/sisyphus/internal/mcpserver"
	"github.com/go-faster/sisyphus/internal/netclient"
	"github.com/go-faster/sisyphus/internal/queue"

	_ "github.com/jackc/pgx/v5/stdlib" // register pgx driver
)

// openJobDB opens ssagent's Postgres connection for InvestigationJob
// persistence. ssagent never runs migrations itself (only ssapi does, see
// internal/wire) — it assumes the investigation_jobs table already exists,
// same as ssingest connecting without migrating.
func openJobDB(dsn string) (*ent.Client, func(), error) {
	db, err := otelsql.Open("pgx", dsn,
		otelsql.WithAttributes(semconv.DBSystemPostgreSQL),
		otelsql.WithSpanOptions(otelsql.SpanOptions{DisableErrSkip: true}),
	)
	if err != nil {
		return nil, nil, errors.Wrap(err, "open db")
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	return client, func() { _ = db.Close() }, nil
}

func run(ctx context.Context, lg *zap.Logger, telemetry *app.Telemetry, info cliversion.Info) error {
	ctx = zctx.Base(ctx, lg)
	cfg, err := config.Load()
	if err != nil {
		return errors.Wrap(err, "load config")
	}
	cfg.LogWarnings(lg)

	if cfg.Agent.AuthToken == "" {
		return errors.New("agent.auth_token is required")
	}
	if cfg.DatabaseDSN == "" {
		return errors.New("database.dsn is required (ssagent persists /investigate jobs in Postgres)")
	}

	db, closeDB, err := openJobDB(cfg.DatabaseDSN)
	if err != nil {
		return errors.Wrap(err, "open job db")
	}
	defer closeDB()
	jobTimeout := time.Duration(cfg.Agent.RequestTimeoutSeconds) * time.Second
	if jobTimeout == 0 {
		jobTimeout = 3 * time.Minute
	}

	hostname, _ := os.Hostname()
	store := agentstore.New(db, agentstore.Options{
		// The lease IS the job timeout: queue.Worker bounds each handler by
		// its claim, so a job can never still be running once another replica
		// is free to take it.
		Lease: jobTimeout,
		Owner: hostname,
	})

	// Only settles jobs the queue itself gave up on. A job another replica is
	// still working on is left alone — with a shared queue, "running" no
	// longer implies "running here".
	if n, err := store.ReapStale(ctx); err != nil {
		lg.Error("reap stale jobs", zap.Error(err))
	} else if n > 0 {
		lg.Warn("reaped stale jobs abandoned by a previous run", zap.Int("count", n))
	}

	httpClient, err := netclient.HTTPClient(ctx, "openrouter", cfg.Proxies.OpenRouter, netclient.HTTPClientOptions{
		TracerProvider: telemetry.TracerProvider(),
		MeterProvider:  telemetry.MeterProvider(),
		UserAgent:      info.UserAgent("ssagent"),
	})
	if err != nil {
		return errors.Wrap(err, "openrouter http client")
	}
	mcpHTTPClient, err := netclient.HTTPClient(ctx, "mcp", "", netclient.HTTPClientOptions{
		TracerProvider: telemetry.TracerProvider(),
		MeterProvider:  telemetry.MeterProvider(),
		UserAgent:      info.UserAgent("ssagent"),
	})
	if err != nil {
		return errors.Wrap(err, "mcp http client")
	}
	llm := openrouter.New(cfg.OpenRouter.APIKey, openrouter.Options{
		HTTPClient:      httpClient,
		TracerProvider:  telemetry.TracerProvider(),
		MeterProvider:   telemetry.MeterProvider(),
		ReasoningEffort: cfg.OpenRouter.ReasoningEffort,
	})

	mcpOpts := mcpclient.Options{
		URL:           cfg.Agent.GatewayURL,
		MeterProvider: telemetry.MeterProvider(),
		HTTPClient:    mcpHTTPClient,
		Version:       info.Short(),
	}
	mClient, err := mcpclient.New(ctx, mcpOpts)
	if err != nil {
		return errors.Wrap(err, "mcp client new")
	}
	defer func() {
		if err := mClient.Close(); err != nil {
			lg.Error("close mcp client", zap.Error(err))
		}
	}()

	model := cfg.Agent.Model
	if model == "" {
		model = cfg.OpenRouter.Model
	}

	inv := agent.NewInvestigator(llm, mClient, model, agent.InvestigatorOptions{
		MaxIterations:  cfg.Agent.MaxToolIterations,
		MaxReportChars: cfg.Agent.MaxReportChars,
		ShowDebugInfo:  cfg.Agent.ShowDebugInfo,
		Logger:         lg,
	})

	mux := http.NewServeMux()
	mcpserver.InstallHealth(mux, info.Short(), mClient)

	tracer := telemetry.TracerProvider().Tracer("github.com/go-faster/sisyphus/cmd/ssagent")
	metrics, err := newAgentMetrics(telemetry.MeterProvider())
	if err != nil {
		return errors.Wrap(err, "agent metrics")
	}
	maxConcurrent := cfg.Agent.MaxConcurrent
	if maxConcurrent == 0 {
		maxConcurrent = 4
	}
	maxBodyBytes := cfg.Agent.MaxBodyBytes
	if maxBodyBytes == 0 {
		maxBodyBytes = 64 * 1024
	}

	auth := mcpserver.BearerAuthMiddleware(cfg.Agent.AuthToken)
	mux.Handle("POST /investigate", auth(handleInvestigateSubmit(store, maxBodyBytes, lg)))
	mux.Handle("GET /investigate/{id}", auth(handleInvestigateGet(store, lg)))

	srv := &http.Server{
		Addr:              cfg.Agent.Addr,
		Handler:           httpmw.Wrap(lg, telemetry, mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// The worker drains the queue alongside the HTTP server rather than being
	// dispatched from the request handler, so an investigation outlives the
	// request — and the process — that submitted it.
	// No job timeout here: the claim's lease is the timeout (see
	// agentstore.Options.Lease below), so an investigation cannot outlive the
	// claim that authorizes it.
	worker := queue.NewWorker(store.Queue(),
		investigateHandler(store, inv, tracer, metrics, lg),
		queue.WorkerOptions{
			Concurrency: maxConcurrent,
			Logger:      lg,
		})

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return worker.Run(gctx) })
	g.Go(func() error { return httpmw.Serve(gctx, lg, "ssagent", srv) })
	return g.Wait()
}

func main() {
	app.Run(func(ctx context.Context, lg *zap.Logger, telemetry *app.Telemetry) error {
		ctx = zctx.Base(ctx, lg)
		info, _ := cliversion.GetInfo("github.com/go-faster/sisyphus")
		cmd := &cobra.Command{
			Use:   "ssagent",
			Short: "runs the investigation service",
			RunE: func(cmd *cobra.Command, _ []string) error {
				return run(cmd.Context(), lg, telemetry, info)
			},
			SilenceUsage:  true,
			SilenceErrors: true,
		}
		cmdutil.ConfigureVersion(cmd, info)
		cmd.AddCommand(cmdutil.NewVersionCmd("ssagent", info))
		cmd.SetContext(ctx)
		return cmd.Execute()
	}, app.WithServiceName("ssagent"), app.WithServiceNamespace("sisyphus"))
}
