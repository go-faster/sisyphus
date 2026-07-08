package main

import (
	"context"
	"net/http"
	"time"

	"github.com/go-faster/errors"
	"go.uber.org/zap"

	"github.com/go-faster/sdk/app"
	"github.com/go-faster/sdk/zctx"

	"github.com/go-faster/sisyphus/internal/agent"
	"github.com/go-faster/sisyphus/internal/config"
	"github.com/go-faster/sisyphus/internal/llm/openrouter"
	"github.com/go-faster/sisyphus/internal/mcpclient"
	"github.com/go-faster/sisyphus/internal/mcpserver"
)

func run(ctx context.Context, lg *zap.Logger, _ *app.Telemetry) error {
	ctx = zctx.Base(ctx, lg)
	cfg, err := config.Load()
	if err != nil {
		return errors.Wrap(err, "load config")
	}

	if cfg.Agent.AuthToken == "" {
		return errors.New("agent.auth_token is required")
	}

	llm := openrouter.New(cfg.OpenRouter.APIKey, openrouter.Options{})

	mcpOpts := mcpclient.Options{
		URL: cfg.Agent.GatewayURL,
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

	inv := agent.NewInvestigator(llm, mClient, model, cfg.Agent.MaxToolIterations, lg)

	mux := http.NewServeMux()
	mux.Handle("/health", mcpserver.HealthHandler("ssagent"))
	mux.Handle("/healthz", mcpserver.HealthHandler("ssagent"))
	mux.Handle("/ready", mcpserver.ReadinessHandler(mClient))
	mux.Handle("/readyz", mcpserver.ReadinessHandler(mClient))

	timeout := time.Duration(cfg.Agent.RequestTimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 3 * time.Minute
	}

	mux.Handle("/investigate", mcpserver.BearerAuthMiddleware(cfg.Agent.AuthToken)(handleInvestigate(inv, timeout, lg)))

	srv := &http.Server{
		Addr:              cfg.Agent.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	lg.Info("starting ssagent", zap.String("addr", cfg.Agent.Addr))

	errc := make(chan error, 1)
	go func() {
		errc <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		return srv.Shutdown(context.Background())
	case err := <-errc:
		return err
	}
}

func main() {
	app.Run(run, app.WithServiceName("ssagent"), app.WithServiceNamespace("sisyphus"))
}
