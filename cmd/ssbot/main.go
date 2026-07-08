// Command ssbot runs the Telegram /context bot against the sisyphus API.
package main

import (
	"context"
	"net/http"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/app"
	"github.com/go-faster/sdk/zctx"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/agentclient"
	"github.com/go-faster/sisyphus/internal/apiclient"
	"github.com/go-faster/sisyphus/internal/bot"
	"github.com/go-faster/sisyphus/internal/config"
	"github.com/go-faster/sisyphus/internal/mcpserver"
	"github.com/go-faster/sisyphus/internal/netclient"
)

func main() {
	app.Run(
		func(ctx context.Context, lg *zap.Logger, t *app.Telemetry) error {
			ctx = zctx.Base(ctx, lg)
			cmd := &cobra.Command{
				Use:   "ssbot",
				Short: "runs the Telegram /context bot against the sisyphus API",
				RunE: func(cmd *cobra.Command, _ []string) error {
					cfg, err := config.Load()
					if err != nil {
						return errors.Wrap(err, "config")
					}
					return run(cmd.Context(), cfg, t.TracerProvider(), t.MeterProvider())
				},
				SilenceUsage:  true,
				SilenceErrors: true,
			}
			cmd.SetContext(ctx)
			return cmd.Execute()
		},
		app.WithServiceName("ssbot"),
		app.WithServiceNamespace("sisyphus"),
	)
}

func run(ctx context.Context, cfg config.Config, tp trace.TracerProvider, mp metric.MeterProvider) error {
	lg := zctx.From(ctx)
	if cfg.API.BaseURL == "" || cfg.API.AuthToken == "" {
		return errors.New("api.base_url and api.auth_token are required")
	}
	if cfg.Telegram.AppID == 0 || cfg.Telegram.AppHash == "" || cfg.Telegram.BotToken == "" {
		return errors.New("telegram credentials missing")
	}

	httpClient, err := netclient.HTTPClient(ctx, "ssapi", "", netclient.HTTPClientOptions{
		TracerProvider: tp,
		MeterProvider:  mp,
	})
	if err != nil {
		return errors.Wrap(err, "http client")
	}

	api, err := apiclient.New(cfg.API.BaseURL, cfg.API.AuthToken, apiclient.Options{
		HTTPClient:     httpClient,
		TracerProvider: tp,
		MeterProvider:  mp,
	})
	if err != nil {
		return errors.Wrap(err, "api client")
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var investigator bot.Investigator
	var checks []mcpserver.HealthChecker
	checks = append(checks, api)

	if cfg.Agent.BaseURL != "" {
		ag := agentclient.New(agentclient.Options{
			URL:   cfg.Agent.BaseURL,
			Token: cfg.Agent.AuthToken,
		})
		investigator = ag
		checks = append(checks, ag)
	}

	healthMux := newHealthMux(checks...)
	healthSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           healthMux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		lg.Info("health listening", zap.String("addr", cfg.HTTPAddr))
		if err := healthSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- errors.Wrap(err, "health serve")
			cancel()
		}
	}()

	b := bot.New(ctx,
		api,
		api,
		bot.BotCredentials{
			AppID:      cfg.Telegram.AppID,
			AppHash:    cfg.Telegram.AppHash,
			BotToken:   cfg.Telegram.BotToken,
			SessionDir: cfg.Telegram.SessionDir,
		},
		bot.BotOptions{
			Silent:         cfg.Telegram.Silent,
			TracerProvider: tp,
			MeterProvider:  mp,
			Logger:         zctx.From(ctx),
			AllowedChats:   cfg.Telegram.AllowedChats,
			AllowedUserIDs: cfg.Telegram.AllowedUserIDs,
			Investigator:   investigator,
		},
	)

	botErr := b.Run(ctx)
	if err := shutdownHealth(healthSrv); err != nil {
		return err
	}
	select {
	case err := <-errCh:
		if err != nil {
			return err
		}
	default:
	}
	return botErr
}

func shutdownHealth(srv *http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func newHealthMux(checks ...mcpserver.HealthChecker) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/health", mcpserver.HealthHandler("0.1.0"))
	mux.Handle("/healthz", mcpserver.HealthHandler("0.1.0"))
	mux.Handle("/ready", mcpserver.ReadinessHandler(checks...))
	mux.Handle("/readyz", mcpserver.ReadinessHandler(checks...))
	return mux
}
