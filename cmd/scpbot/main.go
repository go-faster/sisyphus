// Command scpbot runs the ingestion/index API and the Telegram /context bot.
package main

import (
	"context"
	"net/http"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/app"
	"github.com/go-faster/sdk/zctx"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/go-faster/scpbot/internal/api"
	"github.com/go-faster/scpbot/internal/bot"
	"github.com/go-faster/scpbot/internal/config"
	"github.com/go-faster/scpbot/internal/oas"
	"github.com/go-faster/scpbot/internal/wire"
)

func main() {
	app.Run(
		func(ctx context.Context, lg *zap.Logger, t *app.Telemetry) error {
			ctx = zctx.Base(ctx, lg)
			cfg, err := config.Load()
			if err != nil {
				return errors.Wrap(err, "config")
			}
			return run(ctx, cfg, t.TracerProvider(), t.MeterProvider())
		},
		app.WithServiceName("scpmcp"),
		app.WithServiceNamespace("scpbot"),
	)
}

func run(ctx context.Context, cfg config.Config, tp trace.TracerProvider, mp metric.MeterProvider) error {
	lg := zctx.From(ctx)

	comp, err := wire.New(ctx, cfg, wire.NewOptions{
		TracerProvider: tp,
		MeterProvider:  mp,
	})
	if err != nil {
		return err
	}
	defer comp.Close()

	// HTTP API.
	handler := api.New(comp.Retriever, comp.Answerer, "0.1.0")
	srv, err := oas.NewServer(handler,
		oas.WithTracerProvider(tp),
		oas.WithMeterProvider(mp),
	)
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
		b := bot.New(ctx, bot.Config{
			AppID:      cfg.Telegram.AppID,
			AppHash:    cfg.Telegram.AppHash,
			BotToken:   cfg.Telegram.BotToken,
			SessionDir: cfg.Telegram.SessionDir,
		}, comp.Retriever, comp.Answerer, tp)
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
