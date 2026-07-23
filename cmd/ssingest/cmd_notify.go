package main

import (
	"context"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/config"
	"github.com/go-faster/sisyphus/internal/ent"
	gitlabingest "github.com/go-faster/sisyphus/internal/ingest/gitlab"
	jiraingest "github.com/go-faster/sisyphus/internal/ingest/jira"
	"github.com/go-faster/sisyphus/internal/ingestrun"
	"github.com/go-faster/sisyphus/internal/netclient"
	"github.com/go-faster/sisyphus/internal/notify"
	notifygitlab "github.com/go-faster/sisyphus/internal/notify/gitlab"
	notifyjira "github.com/go-faster/sisyphus/internal/notify/jira"
	notifystore "github.com/go-faster/sisyphus/internal/notify/store"
)

// SyncState sources for the notify collectors' cursors. Distinct from the
// ingestion sources (index.SourceGitLabMR etc.) so the notify poll cadence
// and diff state never interact with ingestion's.
const (
	syncSourceNotifyGitLab = "notify_gitlab"
	syncSourceNotifyJira   = "notify_jira"
)

// notifyRunner builds and runs the notify collectors + dispatcher: one pass
// over GitLab MR assignment/review-request events and Jira issue-assignment
// events, writing matched users' outbox rows via internal/notify/store.
// Registered as ssingest serve's "notify" trigger key (cmd_serve.go),
// alongside gitlab/jira/git/files/telegram.
type notifyRunner struct {
	db        *ent.Client
	cfg       config.Config
	tp        trace.TracerProvider
	mp        metric.MeterProvider
	userAgent string
}

func (d *ingestDeps) notifyRunner() notifyRunner {
	return notifyRunner{
		db:        d.services.DB,
		cfg:       d.cfg,
		tp:        d.tp,
		mp:        d.mp,
		userAgent: d.userAgent,
	}
}

// RunOnce collects and dispatches both sources. A source with no
// credentials configured is skipped, not an error; a real collection error
// for one source does not prevent the other source from still running.
func (r notifyRunner) RunOnce(ctx context.Context) error {
	lg := zctx.From(ctx).Named("notify")
	store := notifystore.New(r.db, notifystore.Options{Owner: "ssingest"})
	dispatcher := notify.NewDispatcher(store, store, notify.ChannelTelegram, nil)

	var errs []error
	if err := r.collectGitLab(ctx, lg, dispatcher); err != nil && !errors.Is(err, errNotConfigured) {
		lg.Error("gitlab notify collection failed", zap.Error(err))
		errs = append(errs, err)
	}
	if err := r.collectJira(ctx, lg, dispatcher); err != nil && !errors.Is(err, errNotConfigured) {
		lg.Error("jira notify collection failed", zap.Error(err))
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (r notifyRunner) collectGitLab(ctx context.Context, lg *zap.Logger, dispatcher *notify.Dispatcher) error {
	cfg := r.cfg.GitLab
	projects := ingestrun.GitLabProjectRefs(cfg.Projects)
	if cfg.BaseURL == "" || cfg.Token == "" || len(projects) == 0 {
		return errNotConfigured
	}

	cache, err := ingestrun.AuthenticatedHTTPCache("gitlab-notify", cfg.BaseURL, cfg.Token)
	if err != nil {
		return errors.Wrap(err, "gitlab notify http cache")
	}
	httpClient, err := netclient.HTTPClient(ctx, "gitlab-notify", r.cfg.Proxies.GitLab, netclient.HTTPClientOptions{
		TracerProvider: r.tp,
		MeterProvider:  r.mp,
		Cache:          cache,
		UserAgent:      r.userAgent,
	})
	if err != nil {
		return errors.Wrap(err, "gitlab notify http client")
	}
	fetcher, err := gitlabingest.New(gitlabingest.Options{
		BaseURL:    cfg.BaseURL,
		Token:      cfg.Token,
		Projects:   projects,
		HTTPClient: httpClient,
		UserAgent:  r.userAgent,
	})
	if err != nil {
		return errors.Wrap(err, "gitlab notify new fetcher")
	}

	cursor, err := ingestrun.LoadRawCursor(ctx, r.db, syncSourceNotifyGitLab)
	if err != nil {
		return err
	}

	collector := notifygitlab.New(fetcher)
	events, nextCursor, err := collector.Collect(ctx, cursor)
	if err != nil {
		_ = ingestrun.UpsertSyncState(ctx, r.db, syncSourceNotifyGitLab, time.Now(), cursor, "error", 0)
		return errors.Wrap(err, "collect gitlab events")
	}

	enqueued, err := dispatcher.Dispatch(ctx, events)
	if err != nil {
		_ = ingestrun.UpsertSyncState(ctx, r.db, syncSourceNotifyGitLab, time.Now(), cursor, "error", 0)
		return errors.Wrap(err, "dispatch gitlab events")
	}

	if err := ingestrun.UpsertSyncState(ctx, r.db, syncSourceNotifyGitLab, time.Now(), nextCursor, "ok", enqueued); err != nil {
		return errors.Wrap(err, "upsert gitlab notify syncstate")
	}
	lg.Info("gitlab notify collection done", zap.Int("events", len(events)), zap.Int("enqueued", enqueued))
	return nil
}

func (r notifyRunner) collectJira(ctx context.Context, lg *zap.Logger, dispatcher *notify.Dispatcher) error {
	cfg := r.cfg.Jira
	projects := ingestrun.JiraProjectKeys(cfg.Projects)
	noCreds := cfg.PAT == "" && (cfg.Username == "" || cfg.Password == "") && (cfg.Email == "" || cfg.APIToken == "")
	if cfg.BaseURL == "" || noCreds || len(projects) == 0 {
		return errNotConfigured
	}

	cache, err := ingestrun.AuthenticatedHTTPCache("jira-notify", cfg.BaseURL, cfg.Email, cfg.Username, cfg.APIToken, cfg.Password, cfg.PAT)
	if err != nil {
		return errors.Wrap(err, "jira notify http cache")
	}
	httpClient, err := netclient.HTTPClient(ctx, "jira-notify", r.cfg.Proxies.Jira, netclient.HTTPClientOptions{
		TracerProvider: r.tp,
		MeterProvider:  r.mp,
		Cache:          cache,
		UserAgent:      r.userAgent,
	})
	if err != nil {
		return errors.Wrap(err, "jira notify http client")
	}
	fetcher, err := jiraingest.New(jiraingest.Options{
		BaseURL:    cfg.BaseURL,
		Email:      cfg.Email,
		Username:   cfg.Username,
		APIToken:   cfg.APIToken,
		Password:   cfg.Password,
		PAT:        cfg.PAT,
		HTTPClient: httpClient,
		UserAgent:  r.userAgent,
	})
	if err != nil {
		return errors.Wrap(err, "jira notify new fetcher")
	}

	cursor, err := ingestrun.LoadRawCursor(ctx, r.db, syncSourceNotifyJira)
	if err != nil {
		return err
	}

	collector := notifyjira.New(fetcher, projects)
	events, nextCursor, err := collector.Collect(ctx, cursor)
	if err != nil {
		_ = ingestrun.UpsertSyncState(ctx, r.db, syncSourceNotifyJira, time.Now(), cursor, "error", 0)
		return errors.Wrap(err, "collect jira events")
	}

	enqueued, err := dispatcher.Dispatch(ctx, events)
	if err != nil {
		_ = ingestrun.UpsertSyncState(ctx, r.db, syncSourceNotifyJira, time.Now(), cursor, "error", 0)
		return errors.Wrap(err, "dispatch jira events")
	}

	if err := ingestrun.UpsertSyncState(ctx, r.db, syncSourceNotifyJira, time.Now(), nextCursor, "ok", enqueued); err != nil {
		return errors.Wrap(err, "upsert jira notify syncstate")
	}
	lg.Info("jira notify collection done", zap.Int("events", len(events)), zap.Int("enqueued", enqueued))
	return nil
}
