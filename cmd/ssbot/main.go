// Command ssbot runs the Telegram /context bot against the sisyphus API.
package main

import (
	"context"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/app"
	"github.com/go-faster/sdk/zctx"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/apiclient"
	"github.com/go-faster/sisyphus/internal/bot"
	"github.com/go-faster/sisyphus/internal/config"
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
	if cfg.API.BaseURL == "" || cfg.API.AuthToken == "" {
		return errors.New("api.base_url and api.auth_token are required")
	}
	if cfg.Telegram.AppID == 0 || cfg.Telegram.BotToken == "" {
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
		},
	)
	return b.Run(ctx)
}
