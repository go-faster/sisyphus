package main

import (
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"github.com/spf13/cobra"
	"go.uber.org/zap"

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
			"first.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			lg := zctx.From(ctx)
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			grace, _ := cmd.Flags().GetDuration("grace")
			batch, _ := cmd.Flags().GetInt("batch")

			if deps.services.Vectors == nil {
				return errors.New("gc: vector store unavailable")
			}
			points, ok := deps.services.Vectors.(vectorgc.PointStore)
			if !ok {
				return errors.Errorf("gc: vector store %T cannot scan points", deps.services.Vectors)
			}

			c, err := vectorgc.New(points, vectorgc.NewEntRefStore(deps.services.DB), vectorgc.Options{
				Grace:  grace,
				Batch:  batch,
				DryRun: dryRun,
				Logger: lg,
			})
			if err != nil {
				return err
			}

			rep, err := c.Run(ctx)
			// Report whatever the run reached, including on failure: a partial
			// sweep is still progress, and the next run re-finds the rest.
			lg.Info("vector gc report",
				zap.Int("scanned", rep.Scanned),
				zap.Int("candidates", rep.Candidates),
				zap.Int("spared", rep.Spared),
				zap.Int("deleted", rep.Deleted),
				zap.Bool("dry_run", rep.DryRun),
			)
			if err != nil {
				return errors.Wrap(err, "gc")
			}
			return nil
		},
	}
	cmd.Flags().Duration("grace", 5*time.Minute,
		"how long a point must look orphaned before deleting it (0 uses the default; covers in-flight indexing)")
	cmd.Flags().Int("batch", 1024, "scan/delete page size")
	return cmd
}
