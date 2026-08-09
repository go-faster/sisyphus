package main

import (
	"context"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/ingestrun"
	"github.com/go-faster/sisyphus/internal/maint"
	"github.com/go-faster/sisyphus/internal/reconcile"
)

func newReconcileCmd(deps *ingestDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "delete indexed documents whose upstream object is gone",
		Long: "Detect upstream deletions.\n\n" +
			"Incremental ingestion is cursor-based, and a deleted issue produces no update —\n" +
			"it just stops existing — so nothing else notices. This lists every object in each\n" +
			"configured GitLab and Jira project and deletes indexed documents that are no\n" +
			"longer there.\n\n" +
			"It is the only command that deletes indexed content on the evidence of an\n" +
			"absence, and an absence upstream is not always a deletion: a revoked token, a\n" +
			"renamed project or an account that lost access all read the same way. A scope\n" +
			"whose diff is too large to be a deletion is refused rather than applied.\n\n" +
			"RUN IT WITH --dry-run FIRST and read what it would remove.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			fraction, _ := cmd.Flags().GetFloat64("max-delete-fraction")
			minIndexed, _ := cmd.Flags().GetInt("min-indexed")

			return deps.runMaintOnce(ctx, maint.JobReconcile, deps.reconcileJob(reconcile.Options{
				MaxDeleteFraction:     fraction,
				MinIndexedForFraction: minIndexed,
				DryRun:                dryRun,
			}))
		},
	}
	cmd.Flags().Float64("max-delete-fraction", 0.2,
		"refuse a scope whose diff would delete more than this share of its indexed documents")
	cmd.Flags().Int("min-indexed", 20,
		"documents a scope needs before --max-delete-fraction applies")
	return cmd
}

// reconcileJob returns one reconciliation pass.
//
// Scopes are resolved per run, not at startup: a project added to config, or a
// token that started working again, must take effect on the next sweep rather
// than on the next restart. It also means an unconfigured source is a no-op
// run instead of a failure — there is nothing to reconcile against.
func (d *ingestDeps) reconcileJob(opts reconcile.Options) func(context.Context) error {
	return func(ctx context.Context) error {
		scopes, err := d.reconcileScopes(ctx)
		if err != nil {
			return err
		}
		if len(scopes) == 0 {
			zctx.From(ctx).Info("reconcile: no configured sources to reconcile")
			return nil
		}
		store := reconcile.NewEntStore(d.services.DB, d.services.Vectors)
		return maint.Reconcile(ctx, store, scopes, opts)
	}
}

// reconcileScopes builds a scope per configured GitLab/Jira project.
//
// A source that is not configured contributes no scopes, and contributes no
// error either: reconciliation is defined against what ingestion actually
// indexes, and an install with only GitLab must not fail because Jira is unset.
func (d *ingestDeps) reconcileScopes(ctx context.Context) ([]reconcile.Scope, error) {
	lg := zctx.From(ctx)
	runner := ingestrun.Runner{
		DB:        d.services.DB,
		Vectors:   d.services.Vectors,
		Embedder:  d.services.Embedder,
		Config:    d.cfg,
		TP:        d.tp,
		MP:        d.mp,
		UserAgent: d.userAgent,
	}

	var scopes []reconcile.Scope

	glFetcher, glProjects, err := runner.NewGitLabFetcher(ctx)
	switch {
	case errors.Is(err, ingestrun.ErrNotConfigured):
		lg.Debug("reconcile: gitlab not configured, skipping")
	case err != nil:
		return nil, errors.Wrap(err, "gitlab fetcher")
	default:
		cfg := d.cfg.GitLab
		scopes = append(scopes, reconcile.GitLabScopes(glFetcher, glProjects,
			cfg.Issues, cfg.MergeRequests, cfg.Releases)...)
	}

	jiraFetcher, jiraProjects, _, err := runner.NewJiraFetcher(ctx)
	switch {
	case errors.Is(err, ingestrun.ErrNotConfigured):
		lg.Debug("reconcile: jira not configured, skipping")
	case err != nil:
		return nil, errors.Wrap(err, "jira fetcher")
	default:
		scopes = append(scopes, reconcile.JiraScopes(jiraFetcher, jiraProjects)...)
	}

	lg.Info("reconcile: scopes resolved", zap.Int("scopes", len(scopes)))
	return scopes, nil
}
