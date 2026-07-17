package main

import (
	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"github.com/spf13/cobra"
	"go.uber.org/zap"

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
			"count them first.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			lg := zctx.From(ctx)
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			batch, _ := cmd.Flags().GetInt("batch")

			if deps.services.Vectors == nil {
				return errors.New("repair: vector store unavailable")
			}
			r, err := vectorrepair.New(deps.services.DB, deps.services.Embedder, deps.services.Vectors,
				vectorrepair.Options{
					Batch:  batch,
					DryRun: dryRun,
					Logger: lg,
				})
			if err != nil {
				return err
			}

			rep, err := r.Run(ctx)
			lg.Info("vector repair report",
				zap.Int("mismatched", rep.Mismatched),
				zap.Int("repaired", rep.Repaired),
				zap.Bool("dry_run", rep.DryRun),
			)
			if err != nil {
				return errors.Wrap(err, "repair")
			}
			return nil
		},
	}
	cmd.Flags().Int("batch", 64, "how many chunks to re-embed at a time")
	return cmd
}
