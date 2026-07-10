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
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/api"
	"github.com/go-faster/sisyphus/internal/config"
	"github.com/go-faster/sisyphus/internal/httpmw"
	"github.com/go-faster/sisyphus/internal/mcpserver"
	"github.com/go-faster/sisyphus/internal/oas"
	"github.com/go-faster/sisyphus/internal/webhook"
	"github.com/go-faster/sisyphus/internal/wire"
)

func main() {
	app.Run(
		func(ctx context.Context, lg *zap.Logger, t *app.Telemetry) error {
			ctx = zctx.Base(ctx, lg)
			cmd := &cobra.Command{
				Use:   "ssapi",
				Short: "runs the hybrid-search HTTP API (owns DB + migrations)",
				RunE: func(cmd *cobra.Command, _ []string) error {
					cfg, err := config.Load()
					if err != nil {
						return errors.Wrap(err, "config")
					}
					cfg.LogWarnings(lg)
					return run(cmd.Context(), cfg, t.TracerProvider(), t.MeterProvider())
				},
				SilenceUsage:  true,
				SilenceErrors: true,
			}
			cmd.SetContext(ctx)
			return cmd.Execute()
		},
		app.WithServiceName("ssapi"),
		app.WithServiceNamespace("sisyphus"),
	)
}

func run(ctx context.Context, cfg config.Config, tp trace.TracerProvider, mp metric.MeterProvider) error {
	lg := zctx.From(ctx)

	comp, err := wire.New(ctx, cfg, wire.NewOptions{
		TracerProvider: tp,
		MeterProvider:  mp,
		RunMigrations:  true,
	})
	if err != nil {
		return err
	}
	defer comp.Close()

	if cfg.API.AuthToken == "" {
		return errors.New("api.auth_token is required")
	}

	handler := api.New(comp.Retriever, comp.Answerer, "0.1.0",
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

	trigger := webhook.NewTrigger(ctx, webhook.TriggerOptions{
		Logger:        lg,
		MeterProvider: mp,
	})

	poller := webhook.NewPoller(trigger, lg)
	if comp.DB != nil {
		worker := webhook.NewWorker(comp.DB, comp.Vectors, comp.Embedder, cfg, webhook.WorkerOptions{
			Logger:         lg,
			TracerProvider: tp,
			MeterProvider:  mp,
		})
		trigger.Register("gitlab", worker.RunGitLab)
		trigger.Register("jira", worker.RunJira)

		poller.Start(ctx, "gitlab", time.Duration(cfg.GitLab.PollIntervalSeconds)*time.Second)
		poller.Start(ctx, "jira", time.Duration(cfg.Jira.PollIntervalSeconds)*time.Second)
	}

	mux := http.NewServeMux()
	mcpserver.InstallHealth(mux, "0.1.0", comp.Health)

	if cfg.GitLab.WebhookEnabled && cfg.GitLab.WebhookSecret != "" {
		mux.Handle("POST /webhooks/gitlab", webhook.NewGitLabHandler(cfg.GitLab.WebhookSecret, trigger))
		lg.Info("gitlab webhook enabled", zap.String("path", "/webhooks/gitlab"))
	} else {
		lg.Warn("gitlab webhook disabled", zap.Bool("enabled", cfg.GitLab.WebhookEnabled), zap.Bool("has_secret", cfg.GitLab.WebhookSecret != ""))
	}
	if cfg.Jira.WebhookEnabled && cfg.Jira.WebhookSecret != "" {
		mux.Handle("POST /webhooks/jira", webhook.NewJiraHandler(cfg.Jira.WebhookSecret, trigger))
		lg.Info("jira webhook enabled", zap.String("path", "/webhooks/jira"))
	} else {
		lg.Warn("jira webhook disabled", zap.Bool("enabled", cfg.Jira.WebhookEnabled), zap.Bool("has_secret", cfg.Jira.WebhookSecret != ""))
	}

	mux.Handle("/", oasSrv)
	httpSrv := &http.Server{
		Addr:              cfg.API.HTTPAddr,
		Handler:           httpmw.Wrap(lg, mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	err = httpmw.Serve(ctx, lg, "http", httpSrv)

	lg.Info("waiting for in-flight ingestion jobs to drain")
	poller.Wait()
	trigger.Wait()

	return err
}
