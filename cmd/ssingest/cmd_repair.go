package main

import (
	"github.com/spf13/cobra"

	"github.com/go-faster/sisyphus/internal/maint"
	"github.com/go-faster/sisyphus/internal/vectorrepair"
)

func newRepairCmd(deps *ingestDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repair",
		Short: "rebind chunks whose vector point is keyed by the wrong ID",
		Long: "Rebind chunks bound to a point that is not their own ID.\n\n" +
			"A chunk's point must be keyed by the chunk's own ID, because a vector hit\n" +
			"hydrates its text from Postgres by chunk ID. A point stored under any other ID\n" +
			"resolves to empty text: the chunk stays searchable but contributes nothing.\n\n" +
			"Rows drifted when a document was indexed while the vector store was down and\n" +
			"later re-indexed. Indexing no longer does this; repair fixes the rows written\n" +
			"before it stopped, by re-embedding them under the right ID. Use --dry-run to\n" +
			"count them first.\n\n" +
			"`ssingest maint` runs this on a schedule. Both take the same lock, so running\n" +
			"it by hand alongside the daemon is safe.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			batch, _ := cmd.Flags().GetInt("batch")

			return deps.runMaintOnce(ctx, maint.JobRepair, deps.repairJob(vectorrepair.Options{
				Batch:  batch,
				DryRun: dryRun,
			}))
		},
	}
	cmd.Flags().Int("batch", 64, "how many chunks to re-embed at a time")
	return cmd
}
