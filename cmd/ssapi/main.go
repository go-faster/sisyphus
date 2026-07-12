// Command ssapi owns the database and serves the hybrid-search HTTP API.
package main

import (
	"context"
	"net/http"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/app"
	"github.com/go-faster/sdk/zctx"
	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/api"
	"github.com/go-faster/sisyphus/internal/cliversion"
	"github.com/go-faster/sisyphus/internal/cmdutil"
	"github.com/go-faster/sisyphus/internal/config"
	"github.com/go-faster/sisyphus/internal/httpmw"
	"github.com/go-faster/sisyphus/internal/mcpserver"
	"github.com/go-faster/sisyphus/internal/oas"
	"github.com/go-faster/sisyphus/internal/wire"
)

func main() {
	app.Run(
		func(ctx context.Context, lg *zap.Logger, t *app.Telemetry) error {
			ctx = zctx.Base(ctx, lg)
			info, _ := cliversion.GetInfo("github.com/go-faster/sisyphus")
			cmd := &cobra.Command{
				Use:   "ssapi",
				Short: "runs the hybrid-search HTTP API (owns DB + migrations)",
				RunE: func(cmd *cobra.Command, _ []string) error {
					cfg, err := config.Load()
					if err != nil {
						return errors.Wrap(err, "config")
					}
					cfg.LogWarnings(lg)
					return run(cmd.Context(), cfg, t)
				},
				SilenceUsage:  true,
				SilenceErrors: true,
			}
			cmdutil.ConfigureVersion(cmd, info)
			cmd.AddCommand(cmdutil.NewVersionCmd("ssapi", info))
			cmd.SetContext(ctx)
			return cmd.Execute()
		},
		app.WithServiceName("ssapi"),
		app.WithServiceNamespace("sisyphus"),
	)
}

func run(ctx context.Context, cfg config.Config, telemetry *app.Telemetry) error {
	lg := zctx.From(ctx)
	tp := telemetry.TracerProvider()
	mp := telemetry.MeterProvider()
	info, _ := cliversion.GetInfo("github.com/go-faster/sisyphus")

	comp, err := wire.New(ctx, cfg, wire.NewOptions{
		TracerProvider: tp,
		MeterProvider:  mp,
		RunMigrations:  true,
		UserAgent:      info.UserAgent("ssapi"),
	})
	if err != nil {
		return err
	}
	defer comp.Close()

	if cfg.API.AuthToken == "" {
		return errors.New("api.auth_token is required")
	}

	handler := api.New(comp.Retriever, comp.Answerer, info.Short(),
		api.WithAnswerIndexer(comp.Answers),
		api.WithContentResolver(comp.ContentResolver),
		api.WithURLFetcher(comp.URLFetcher),
	)
	sec := api.NewSecurityHandler(cfg.API.AuthToken)
	oasSrv, err := oas.NewServer(handler, sec,
		oas.WithTracerProvider(tp),
		oas.WithMeterProvider(mp),
		oas.WithErrorHandler(api.ErrorHandler),
	)
	if err != nil {
		return errors.Wrap(err, "oas server")
	}

	mux := http.NewServeMux()
	mcpserver.InstallHealth(mux, info.Short(), comp.Health)
	mux.Handle("/", oasSrv)
	httpSrv := &http.Server{
		Addr:              cfg.API.HTTPAddr,
		Handler:           httpmw.Wrap(lg, telemetry, mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	return httpmw.Serve(ctx, lg, "http", httpSrv)
}
