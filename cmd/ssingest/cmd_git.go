package main

import (
	"fmt"
	"os"

	"github.com/go-faster/errors"
	"github.com/spf13/cobra"
)

func newGitCmd() *cobra.Command {
	var noPrune bool

	cmd := &cobra.Command{
		Use:   "git",
		Short: "ingest git repo content + commits",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			limit, _ := cmd.Flags().GetInt("limit")
			resetFlag, _ := cmd.Flags().GetString("reset")
			doReset := resetFlag == "git" || resetFlag == "all"

			r := runner{
				db:       svc.DB,
				vectors:  svc.Vectors,
				cfg:      cfg,
				tp:       globalTP,
				mp:       globalMP,
				embedder: svc.Embedder,
			}
			if err := r.runGit(ctx, doReset, limit, dryRun, !noPrune); err != nil {
				if errors.Is(err, errNotConfigured) {
					fmt.Fprintf(os.Stderr, "git not configured\n")
					os.Exit(1)
					return nil
				}
				return err
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&noPrune, "no-prune", false, "skip orphan cleanup for git (files removed from repo)")
	return cmd
}
