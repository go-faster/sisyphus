package main

import (
	"fmt"
	"os"
	"time"

	"github.com/go-faster/errors"
	"github.com/spf13/cobra"

	chunkmd "github.com/go-faster/scpbot/internal/chunk/markdown"
	"github.com/go-faster/scpbot/internal/pipeline"
)

func newGitLabCmd() *cobra.Command {
	var noPrune bool

	cmd := &cobra.Command{
		Use:   "gitlab",
		Short: "ingest GitLab docs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			limit, _ := cmd.Flags().GetInt("limit")
			resetFlag, _ := cmd.Flags().GetString("reset")
			doReset := resetFlag == "gitlab" || resetFlag == "all"

			ch := chunkmd.New(chunkmd.ChunkerOptions{})
			pipe := pipeline.New(svc.DB, ch, svc.Embedder, svc.Vectors, pipeline.PipelineOptions{
				TracerProvider: globalTP,
				MeterProvider:  globalMP,
			})

			r := runner{
				db:      svc.DB,
				vectors: svc.Vectors,
				cfg:     cfg,
				tp:      globalTP,
				mp:      globalMP,
			}
			if err := r.runGitLab(ctx, pipe, time.Time{}, doReset, limit, dryRun, !noPrune); err != nil {
				if errors.Is(err, errNotConfigured) {
					fmt.Fprintf(os.Stderr, "gitlab not configured\n")
					os.Exit(1)
					return nil
				}
				return err
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&noPrune, "no-prune", false, "skip orphan cleanup for gitlab (files removed from repo)")
	return cmd
}
