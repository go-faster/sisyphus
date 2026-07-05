package main

import (
	"fmt"
	"os"
	"time"

	"github.com/go-faster/errors"
	"github.com/spf13/cobra"

	chunkgitlab "github.com/go-faster/sisyphus/internal/chunk/gitlab"
)

func newGitLabCmd(deps *ingestDeps) *cobra.Command {
	var sinceStr string

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

			ch := chunkgitlab.New()
			pipe, err := deps.pipeline(ch)
			if err != nil {
				return errors.Wrap(err, "build pipeline")
			}

			r := deps.runner()
			if err := r.runGitLabAPI(ctx, pipe, since, doReset, limit, dryRun); err != nil {
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
	return cmd
}
