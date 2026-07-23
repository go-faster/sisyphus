package main

import (
	"context"
	"net/http"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"github.com/go-faster/sisyphus/internal/httpmw"
	"github.com/go-faster/sisyphus/internal/indexjob"
	"github.com/go-faster/sisyphus/internal/ingestrun"
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

	// The daemon publishes index jobs rather than indexing inline: a source
	// walk is one process's job because it advances cursors, but chunking and
	// embedding what it finds is not, and that is where the time goes.
	r := deps.runnerWith(deps.queueIndexers())

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
		return r.runGitLabAPI(ctx, time.Time{}, false, 0, false)
	}))
	trigger.Register("jira", ignoreNotConfigured(func(ctx context.Context) error {
		return r.runJira(ctx, time.Time{}, false, 0, false)
	}))
	trigger.Register("telegram", ignoreNotConfigured(func(ctx context.Context) error {
		return r.runTelegram(ctx, time.Time{}, false, 0, false, nil)
	}))
	notifyRun := deps.notifyRunner()
	trigger.Register("notify", ignoreNotConfigured(notifyRun.RunOnce))

	poller := webhook.NewPoller(trigger, lg)
	poller.Start(ctx, "git", time.Duration(cfg.Ingest.GitPollIntervalSeconds)*time.Second)
	poller.Start(ctx, "files", time.Duration(cfg.Ingest.FilesPollIntervalSeconds)*time.Second)
	poller.Start(ctx, "gitlab", time.Duration(cfg.GitLab.PollIntervalSeconds)*time.Second)
	poller.Start(ctx, "jira", time.Duration(cfg.Jira.PollIntervalSeconds)*time.Second)
	poller.Start(ctx, "telegram", time.Duration(cfg.Ingest.TelegramPollIntervalSeconds)*time.Second)
	poller.Start(ctx, "notify", time.Duration(cfg.Notify.PollIntervalSeconds)*time.Second)

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

	g, gctx := errgroup.WithContext(ctx)

	// Settle index jobs the queue itself gave up on — attempts spent with no
	// live lease — so they stop looking like outstanding backlog. A job another
	// replica is still indexing is left alone.
	//
	// This belongs to `serve` rather than to the workers: serve is the single
	// scheduler, so it runs once per deployment instead of once per replica,
	// and it must still run when indexing has been moved out entirely.
	if n, err := deps.indexQueue().ReapStale(ctx); err != nil {
		lg.Error("reap stale index jobs", zap.Error(err))
	} else if n > 0 {
		lg.Warn("reaped index jobs abandoned by a previous run", zap.Int("count", n))
	}

	// Index in-process as well, unless dedicated workers are deployed. This is
	// what keeps a single-pod install whole: without it, `serve` would publish
	// jobs nothing ever claims and ingestion would stop at the queue.
	if cfg.Ingest.Worker.Enabled {
		worker, err := newIndexWorker(deps, lg)
		if err != nil {
			return err
		}
		lg.Info("indexing in-process",
			zap.Int("concurrency", cfg.Ingest.Worker.Concurrency),
			zap.String("hint", "set ingest.worker.enabled=false once ssingest worker replicas are deployed"))
		g.Go(func() error { return worker.Run(gctx) })
	} else {
		lg.Info("in-process indexing disabled, publishing index jobs only",
			zap.String("queue", indexjob.QueueName))
	}

	g.Go(func() error { return httpmw.Serve(gctx, lg, "http", httpSrv) })
	err := g.Wait()

	lg.Info("waiting for in-flight ingestion jobs to drain")
	poller.Wait()
	trigger.Wait()

	return err
}

// ignoreNotConfigured wraps fn so a run that was skipped rather than failed is
// not reported as a failure.
//
// Two things count as skipped: a source lacking configuration (e.g. no GitLab
// token), which the one-shot subcommands report as log + exit 1 but which must
// not make the daemon spam error logs every poll tick; and a source another
// process holds the advisory lock on, which is the lock working as intended.
func ignoreNotConfigured(fn func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		err := fn(ctx)
		switch {
		case err == nil,
			errors.Is(err, errNotConfigured),
			errors.Is(err, ingestrun.ErrLocked):
			return nil
		default:
			return err
		}
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
