package main

import (
	"github.com/go-faster/errors"
	"github.com/spf13/cobra"
)

func newFilesCmd(deps *ingestDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "files",
		Short: "index configured local context files",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			limit, _ := cmd.Flags().GetInt("limit")
			resetFlag, _ := cmd.Flags().GetString("reset")

			r := deps.runner()
			doReset := resetFlag == "all" || resetFlag == "files" || resetFlag == "context_files"
			if err := r.runFiles(ctx, doReset, limit, dryRun); err != nil {
				return errors.Wrap(err, "files")
			}
			return nil
		},
	}
}
