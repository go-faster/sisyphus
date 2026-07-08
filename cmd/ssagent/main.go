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
	"github.com/go-faster/sisyphus/internal/httpmw"
	"github.com/go-faster/sisyphus/internal/llm/openrouter"
	"github.com/go-faster/sisyphus/internal/mcpclient"
	"github.com/go-faster/sisyphus/internal/mcpserver"
)

func run(ctx context.Context, lg *zap.Logger, telemetry *app.Telemetry) error {
	ctx = zctx.Base(ctx, lg)
	cfg, err := config.Load()
	if err != nil {
		return errors.Wrap(err, "load config")
	}
	cfg.LogWarnings(lg)

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

	inv := agent.NewInvestigator(llm, mClient, model, cfg.Agent.MaxToolIterations, cfg.Agent.MaxReportChars, lg)

	mux := http.NewServeMux()
	mcpserver.InstallHealth(mux, "ssagent", mClient)

	timeout := time.Duration(cfg.Agent.RequestTimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 3 * time.Minute
	}

	tracer := telemetry.TracerProvider().Tracer("github.com/go-faster/sisyphus/ssagent")
	mux.Handle("/investigate", mcpserver.BearerAuthMiddleware(cfg.Agent.AuthToken)(handleInvestigate(inv, timeout, tracer, lg)))

	srv := &http.Server{
		Addr:              cfg.Agent.Addr,
		Handler:           httpmw.Wrap(lg, mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return httpmw.Serve(ctx, lg, "ssagent", srv)
}

func main() {
	app.Run(run, app.WithServiceName("ssagent"), app.WithServiceNamespace("sisyphus"))
}
