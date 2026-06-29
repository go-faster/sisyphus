package main

import (
	"fmt"
	"os"
	"time"

	"github.com/go-faster/errors"
	"github.com/spf13/cobra"

	chunkjira "github.com/go-faster/scpbot/internal/chunk/jira"
	"github.com/go-faster/scpbot/internal/pipeline"
)

func newJiraCmd() *cobra.Command {
	var sinceStr string

	cmd := &cobra.Command{
		Use:   "jira",
		Short: "ingest Jira issues",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			limit, _ := cmd.Flags().GetInt("limit")
			resetFlag, _ := cmd.Flags().GetString("reset")
			doReset := resetFlag == "jira" || resetFlag == "all"

			var since time.Time
			if sinceStr != "" {
				var err error
				since, err = time.Parse(time.RFC3339, sinceStr)
				if err != nil {
					return errors.Wrap(err, "invalid --since")
				}
			}

			ch := chunkjira.New()
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
			if err := r.runJira(ctx, pipe, since, doReset, limit, dryRun); err != nil {
				if errors.Is(err, errNotConfigured) {
					fmt.Fprintf(os.Stderr, "jira not configured\n")
					os.Exit(1)
					return nil
				}
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&sinceStr, "since", "", "RFC3339 override for cursor (jira)")
	return cmd
}
