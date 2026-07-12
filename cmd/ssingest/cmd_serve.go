package main

import (
	"context"
	"net/http"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"github.com/spf13/cobra"
	"go.uber.org/zap"

	chunktg "github.com/go-faster/sisyphus/internal/chunk/telegram"
	"github.com/go-faster/sisyphus/internal/httpmw"
	"github.com/go-faster/sisyphus/internal/mcpserver"
	"github.com/go-faster/sisyphus/internal/webhook"
	"github.com/go-faster/sisyphus/internal/wire"
)

func newServeCmd(deps *ingestDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "run as a daemon: webhook- and poll-triggered incremental ingestion for every configured source",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runServe(cmd.Context(), deps)
		},
	}
}

// runServe wires a webhook.Trigger + webhook.Poller per source (mirroring
// the pattern cmd/ssapi used to run for gitlab/jira only) so ssingest itself
// can run as a long-lived daemon instead of a one-shot CLI. It owns webhook
// HTTP endpoints and polling for ALL sources now, not just gitlab/jira, so
// there is exactly one process scheduling ingestion.
func runServe(ctx context.Context, deps *ingestDeps) error {
	lg := zctx.From(ctx)
	cfg := deps.cfg
	r := deps.runner()

	// Built once and reused across triggers/polls: runGit/runFiles build
	// their own per-content-type pipelines internally, but runTelegram takes
	// its pipeline from the caller (see cmd_telegram.go), so it needs one
	// here too.
	tgPipe, err := deps.pipeline(chunktg.New())
	if err != nil {
		return errors.Wrap(err, "build telegram pipeline")
	}

	trigger := webhook.NewTrigger(ctx, webhook.TriggerOptions{
		Logger:        lg,
		MeterProvider: deps.mp,
	})

	trigger.Register("git", ignoreNotConfigured(func(ctx context.Context) error {
		return r.runGit(ctx, false, 0, false, true)
	}))
	trigger.Register("files", ignoreNotConfigured(func(ctx context.Context) error {
		return r.runFiles(ctx, false, 0, false)
	}))
	trigger.Register("gitlab", ignoreNotConfigured(func(ctx context.Context) error {
		return r.runGitLabAPI(ctx, nil, time.Time{}, false, 0, false)
	}))
	trigger.Register("jira", ignoreNotConfigured(func(ctx context.Context) error {
		return r.runJira(ctx, nil, time.Time{}, false, 0, false)
	}))
	trigger.Register("telegram", ignoreNotConfigured(func(ctx context.Context) error {
		return r.runTelegram(ctx, tgPipe, time.Time{}, false, 0, false, nil)
	}))

	poller := webhook.NewPoller(trigger, lg)
	poller.Start(ctx, "git", time.Duration(cfg.Ingest.GitPollIntervalSeconds)*time.Second)
	poller.Start(ctx, "files", time.Duration(cfg.Ingest.FilesPollIntervalSeconds)*time.Second)
	poller.Start(ctx, "gitlab", time.Duration(cfg.GitLab.PollIntervalSeconds)*time.Second)
	poller.Start(ctx, "jira", time.Duration(cfg.Jira.PollIntervalSeconds)*time.Second)
	poller.Start(ctx, "telegram", time.Duration(cfg.Ingest.TelegramPollIntervalSeconds)*time.Second)

	mux := http.NewServeMux()
	mcpserver.InstallHealth(mux, deps.info.Short(), ingestHealthChecker{deps.services})

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

	httpSrv := &http.Server{
		Addr:              cfg.Ingest.Addr,
		Handler:           httpmw.Wrap(lg, deps.telemetry, mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	err = httpmw.Serve(ctx, lg, "http", httpSrv)

	lg.Info("waiting for in-flight ingestion jobs to drain")
	poller.Wait()
	trigger.Wait()

	return err
}

// ignoreNotConfigured wraps fn so a source lacking configuration (e.g. no
// GitLab token set) is treated as a no-op rather than a poll/webhook
// failure, matching how the one-shot subcommands report it (log + exit 1)
// without making the daemon spam error logs every poll tick.
func ignoreNotConfigured(fn func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := fn(ctx); err != nil && !errors.Is(err, errNotConfigured) {
			return err
		}
		return nil
	}
}

// ingestHealthChecker adapts wire.Services' DB/vector health into
// mcpserver.HealthChecker for ssingest's /ready endpoint.
type ingestHealthChecker struct {
	services *wire.Services
}

func (h ingestHealthChecker) CheckHealth(ctx context.Context) error {
	if h.services == nil {
		return nil
	}
	if h.services.SQLDB != nil {
		if err := h.services.SQLDB.PingContext(ctx); err != nil {
			return errors.Wrap(err, "postgres")
		}
	}
	if h.services.VectorHealth != nil {
		if err := h.services.VectorHealth.CheckHealth(ctx); err != nil {
			return errors.Wrap(err, "qdrant")
		}
	}
	return nil
}
