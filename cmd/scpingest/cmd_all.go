package main

import (
	"strings"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"github.com/spf13/cobra"
	"go.uber.org/zap"

	chunkjira "github.com/go-faster/scpbot/internal/chunk/jira"
	chunkmd "github.com/go-faster/scpbot/internal/chunk/markdown"
	chunktg "github.com/go-faster/scpbot/internal/chunk/telegram"
	"github.com/go-faster/scpbot/internal/index"
	"github.com/go-faster/scpbot/internal/pipeline"
)

func newAllCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "all",
		Short: "run gitlab then jira then telegram in sequence",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			lg := zctx.From(ctx)
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			limit, _ := cmd.Flags().GetInt("limit")
			resetFlag, _ := cmd.Flags().GetString("reset")

			r := runner{
				db:      svc.DB,
				vectors: svc.Vectors,
				cfg:     cfg,
				tp:      globalTP,
				mp:      globalMP,
			}

			if resetFlag == "all" {
				for _, src := range []index.Source{index.SourceGitLabDocs, index.SourceJira, index.SourceTelegram} {
					if err := resetSource(ctx, svc.DB, svc.Vectors, src); err != nil {
						return err
					}
				}
			}

			var failed []string
			// gitlab
			{
				ch := chunkmd.New(chunkmd.ChunkerOptions{})
				pipe := pipeline.New(svc.DB, ch, svc.Embedder, svc.Vectors, pipeline.PipelineOptions{
					TracerProvider: globalTP,
					MeterProvider:  globalMP,
				})
				doReset := resetFlag == "all" || resetFlag == "gitlab"
				if err := r.runGitLab(ctx, pipe, time.Time{}, doReset, limit, dryRun, true); err != nil {
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
				pipe := pipeline.New(svc.DB, ch, svc.Embedder, svc.Vectors, pipeline.PipelineOptions{
					TracerProvider: globalTP,
					MeterProvider:  globalMP,
				})
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
				pipe := pipeline.New(svc.DB, ch, svc.Embedder, svc.Vectors, pipeline.PipelineOptions{
					TracerProvider: globalTP,
					MeterProvider:  globalMP,
				})
				doReset := resetFlag == "all" || resetFlag == "telegram"
				if err := r.runTelegram(ctx, pipe, time.Time{}, doReset, limit, dryRun); err != nil {
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
