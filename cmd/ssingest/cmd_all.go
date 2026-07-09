package main

import (
	"strings"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"github.com/spf13/cobra"
	"go.uber.org/zap"

	chunkgitlab "github.com/go-faster/sisyphus/internal/chunk/gitlab"
	chunkjira "github.com/go-faster/sisyphus/internal/chunk/jira"
	chunktg "github.com/go-faster/sisyphus/internal/chunk/telegram"
	"github.com/go-faster/sisyphus/internal/index"
)

func newAllCmd(deps *ingestDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "all",
		Short: "run git then gitlab then jira then telegram in sequence",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			lg := zctx.From(ctx)
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			limit, _ := cmd.Flags().GetInt("limit")
			resetFlag, _ := cmd.Flags().GetString("reset")

			r := deps.runner()

			if resetFlag == "all" {
				// Reset all configured local context file sources.
				for _, fileSrc := range deps.cfg.ContextFiles {
					if err := resetSource(ctx, deps.services.DB, deps.services.Vectors, index.SourceContextFiles(fileSrc.Name)); err != nil {
						return err
					}
				}
				// Reset all git sources
				for _, gitSrc := range deps.cfg.Git.Repos {
					if err := resetSource(ctx, deps.services.DB, deps.services.Vectors, index.SourceGitDocs(gitSrc.Repo)); err != nil {
						return err
					}
					if gitSrc.Commits {
						if err := resetSource(ctx, deps.services.DB, deps.services.Vectors, index.SourceGitCommit(gitSrc.Repo)); err != nil {
							return err
						}
					}
					if gitSrc.Manifests {
						if err := resetSource(ctx, deps.services.DB, deps.services.Vectors, index.SourceGitManifest(gitSrc.Repo)); err != nil {
							return err
						}
					}
					if gitSrc.Code {
						if err := resetSource(ctx, deps.services.DB, deps.services.Vectors, index.SourceGitCode(gitSrc.Repo)); err != nil {
							return err
						}
					}
				}
				// Reset all gitlab REST sources
				for _, src := range []index.Source{
					index.SourceGitLabIssue,
					index.SourceGitLabMR,
					index.SourceGitLabRelease,
					index.SourceJira,
					index.SourceTelegram,
				} {
					if err := resetSource(ctx, deps.services.DB, deps.services.Vectors, src); err != nil {
						return err
					}
				}
			}

			var failed []string

			// git
			{
				doReset := resetFlag == "all" || resetFlag == "git"
				if err := r.runGit(ctx, doReset, limit, dryRun, true); err != nil {
					if errors.Is(err, errNotConfigured) {
						lg.Info("skipping git (not configured)")
					} else {
						lg.Error("git failed", zap.Error(err))
						failed = append(failed, "git")
					}
				}
			}

			// local context files
			{
				doReset := resetFlag == "all" || resetFlag == "files" || resetFlag == "context_files"
				if err := r.runFiles(ctx, doReset, limit, dryRun); err != nil {
					if errors.Is(err, errNotConfigured) {
						lg.Info("skipping files (not configured)")
					} else {
						lg.Error("files failed", zap.Error(err))
						failed = append(failed, "files")
					}
				}
			}

			// gitlab REST
			{
				ch := chunkgitlab.New()
				pipe, err := deps.pipeline(ch)
				if err != nil {
					return errors.Wrap(err, "build gitlab pipeline")
				}
				doReset := resetFlag == "all" || resetFlag == "gitlab"
				if err := r.runGitLabAPI(ctx, pipe, time.Time{}, doReset, limit, dryRun); err != nil {
					if errors.Is(err, errNotConfigured) {
						lg.Info("skipping gitlab (not configured)")
					} else {
						lg.Error("gitlab failed", zap.Error(err))
						failed = append(failed, "gitlab")
					}
				}
			}

			// jira
			{
				ch := chunkjira.New()
				pipe, err := deps.pipeline(ch)
				if err != nil {
					return errors.Wrap(err, "build jira pipeline")
				}
				doReset := resetFlag == "all" || resetFlag == "jira"
				if err := r.runJira(ctx, pipe, time.Time{}, doReset, limit, dryRun); err != nil {
					if errors.Is(err, errNotConfigured) {
						lg.Info("skipping jira (not configured)")
					} else {
						lg.Error("jira failed", zap.Error(err))
						failed = append(failed, "jira")
					}
				}
			}

			// telegram
			{
				ch := chunktg.New()
				pipe, err := deps.pipeline(ch)
				if err != nil {
					return errors.Wrap(err, "build telegram pipeline")
				}
				doReset := resetFlag == "all" || resetFlag == "telegram"
				if err := r.runTelegram(ctx, pipe, time.Time{}, doReset, limit, dryRun, nil); err != nil {
					if errors.Is(err, errNotConfigured) {
						lg.Info("skipping telegram (not configured)")
					} else {
						lg.Error("telegram failed", zap.Error(err))
						failed = append(failed, "telegram")
					}
				}
			}

			if len(failed) > 0 {
				return errors.New("ingest all failed for: " + strings.Join(failed, ","))
			}
			return nil
		},
	}
}
