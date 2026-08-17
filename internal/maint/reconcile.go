package maint

import (
	"context"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/reconcile"
)

// JobReconcile detects upstream deletions.
const JobReconcile = "reconcile"

// Reconcile diffs every scope against the index and logs the outcome.
//
// A refused scope is not an error: refusing is the guard doing its job, and it
// is reported per scope so an operator can see which one and why. A scope that
// failed to list is an error, because it means deletion detection is not
// happening for that project and nobody would otherwise notice.
func Reconcile(ctx context.Context, store reconcile.Store, scopes []reconcile.Scope, opts reconcile.Options) error {
	r, err := reconcile.New(store, opts)
	if err != nil {
		return err
	}

	rep, runErr := r.Run(ctx, scopes)
	lg := zctx.From(ctx)
	for _, sr := range rep.Scopes {
		fields := []zap.Field{
			zap.String("scope", sr.Scope),
			zap.String("source", string(sr.Source)),
			zap.Int("upstream", sr.Upstream),
			zap.Int("indexed", sr.Indexed),
			zap.Int("missing", sr.Missing),
			zap.Int("deleted", sr.Deleted),
			zap.Bool("dry_run", rep.DryRun),
		}
		switch {
		case sr.Err != nil:
			lg.Error("reconcile scope failed", append(fields, zap.Error(sr.Err))...)
		case sr.Refused:
			lg.Warn("reconcile scope refused", append(fields, zap.String("reason", sr.Reason))...)
		default:
			lg.Info("reconcile scope complete", fields...)
		}
	}

	if runErr != nil {
		return errors.Wrap(runErr, "reconcile")
	}
	return nil
}
