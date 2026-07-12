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
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/agentclient"
	"github.com/go-faster/sisyphus/internal/apiclient"
	"github.com/go-faster/sisyphus/internal/bot"
	"github.com/go-faster/sisyphus/internal/config"
	"github.com/go-faster/sisyphus/internal/httpmw"
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
					cfg.LogWarnings(lg)
					return run(cmd.Context(), cfg, t)
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

func run(ctx context.Context, cfg config.Config, telemetry *app.Telemetry) error {
	lg := zctx.From(ctx)
	tp := telemetry.TracerProvider()
	mp := telemetry.MeterProvider()
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

	healthMux := http.NewServeMux()
	mcpserver.InstallHealth(healthMux, "0.1.0", checks...)
	healthSrv := &http.Server{
		Addr:              cfg.Telegram.Addr,
		Handler:           httpmw.Wrap(lg, telemetry, healthMux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	healthErrc := httpmw.ListenAndServe(lg, "health", healthSrv)
	errCh := make(chan error, 1)
	go func() {
		if err := <-healthErrc; err != nil {
			errCh <- err
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
	if err := httpmw.Shutdown(healthSrv); err != nil {
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
