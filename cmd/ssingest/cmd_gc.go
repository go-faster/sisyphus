package main

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/go-faster/sisyphus/internal/maint"
	"github.com/go-faster/sisyphus/internal/vectorgc"
)

func newGCCmd(deps *ingestDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "delete vector-store points no chunk references",
		Long: "Reclaim orphaned vector-store points.\n\n" +
			"Postgres is the source of truth: a point is garbage exactly when no chunk row\n" +
			"carries its ID. Points leak when the stale-point cleanup during indexing fails\n" +
			"after its transaction has already committed, leaving nothing to retry them.\n\n" +
			"Points are only deleted if they still look orphaned after --grace, because a\n" +
			"document mid-index has its points written before its chunk rows commit and is\n" +
			"otherwise indistinguishable from an orphan. Use --dry-run to see the counts\n" +
			"first.\n\n" +
			"`ssingest maint` runs this on a schedule. Both take the same lock, so running\n" +
			"it by hand alongside the daemon is safe.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			grace, _ := cmd.Flags().GetDuration("grace")
			batch, _ := cmd.Flags().GetInt("batch")

			return deps.runMaintOnce(ctx, maint.JobGC, deps.gcJob(vectorgc.Options{
				Grace:  grace,
				Batch:  batch,
				DryRun: dryRun,
			}))
		},
	}
	cmd.Flags().Duration("grace", 5*time.Minute,
		"how long a point must look orphaned before deleting it (0 uses the default; covers in-flight indexing)")
	cmd.Flags().Int("batch", 1024, "scan/delete page size")
	return cmd
}
