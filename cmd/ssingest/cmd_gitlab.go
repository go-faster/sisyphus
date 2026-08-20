package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/go-faster/errors"
	"github.com/spf13/cobra"
)

func newGitLabCmd(deps *ingestDeps) *cobra.Command {
	var (
		sinceStr  string
		conflicts bool
	)

	cmd := &cobra.Command{
		Use:   "gitlab",
		Short: "ingest GitLab issues, MRs, releases (REST)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			limit, _ := cmd.Flags().GetInt("limit")
			resetFlag, _ := cmd.Flags().GetString("reset")
			doReset := resetFlag == "gitlab" || resetFlag == "all"

			var since time.Time
			if sinceStr != "" {
				var err error
				since, err = time.Parse(time.RFC3339, sinceStr)
				if err != nil {
					return errors.Wrap(err, "invalid --since")
				}
			}

			r := deps.runner()
			run := func(ctx context.Context) error {
				return r.runGitLabAPI(ctx, since, doReset, limit, dryRun)
			}
			if conflicts {
				run = func(ctx context.Context) error { return r.runGitLabConflicts(ctx, dryRun) }
			}
			if err := run(ctx); err != nil {
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
	cmd.Flags().StringVar(&sinceStr, "since", "", "RFC3339 override for cursor (gitlab)")
	cmd.Flags().BoolVar(&conflicts, "conflicts", false, "sweep open MRs for merge conflicts instead of ingesting")
	return cmd
}
