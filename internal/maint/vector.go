package maint

import (
	"context"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/index"
	"github.com/go-faster/sisyphus/internal/vectorgc"
	"github.com/go-faster/sisyphus/internal/vectorrepair"
)

// Job names. They are the metric attribute, the log field and the lock key, so
// the CLI and the scheduler must spell them the same way.
const (
	JobGC     = "gc"
	JobRepair = "repair"
)

// GC runs one vector garbage-collection sweep and logs its report.
//
// The report is logged even when the run failed: a partial sweep is still
// progress, and the next run re-finds the rest.
func GC(ctx context.Context, points vectorgc.PointStore, refs vectorgc.RefStore, opts vectorgc.Options) error {
	c, err := vectorgc.New(points, refs, opts)
	if err != nil {
		return err
	}

	rep, err := c.Run(ctx)
	zctx.From(ctx).Info("vector gc report",
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
}

// Repair rebinds chunks whose vector point is keyed by the wrong ID and logs
// its report.
func Repair(ctx context.Context, db *ent.Client, embedder index.Embedder, vectors vectorrepair.VectorStore, opts vectorrepair.Options) error {
	r, err := vectorrepair.New(db, embedder, vectors, opts)
	if err != nil {
		return err
	}

	rep, err := r.Run(ctx)
	zctx.From(ctx).Info("vector repair report",
		zap.Int("unbound", rep.Unbound),
		zap.Int("repaired", rep.Repaired),
		zap.Bool("dry_run", rep.DryRun),
	)
	if err != nil {
		return errors.Wrap(err, "repair")
	}
	return nil
}
