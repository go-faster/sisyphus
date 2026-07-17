// Package vectorgc reclaims vector-store points that no chunk references.
//
// Postgres is the source of truth: a point is garbage exactly when no row in
// chunks carries its ID in qdrant_point_id. Points are orphaned when the
// stale-point cleanup in internal/pipeline fails after its transaction has
// already committed — the delete is best-effort, so a transient vector-store
// error strands those points with nothing to retry them.
//
// Orphans are not inert. A vector hit is hydrated from Postgres by chunk ID, so
// an orphan resolves to empty text, takes a candidate slot, and can reach an
// answer as a source with no body.
package vectorgc

import (
	"context"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// PointStore is the subset of the vector store the collector needs.
type PointStore interface {
	// ScanPointIDs walks every point in the collection in batches.
	ScanPointIDs(ctx context.Context, batch int, fn func([]uuid.UUID) error) error
	// Delete removes points by ID.
	Delete(ctx context.Context, ids []uuid.UUID) error
}

// RefStore reports which point IDs are still referenced by a chunk.
type RefStore interface {
	// ReferencedPoints returns the subset of ids that some chunk still points at.
	ReferencedPoints(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]bool, error)
}

// Options configures a Collector.
type Options struct {
	// Grace is how long a point must look orphaned before it is deleted. It
	// exists to cover the pipeline's embed-then-persist window: a point is
	// upserted to the vector store before its chunk row commits, so a point
	// belonging to an in-flight document is indistinguishable from an orphan
	// until that row lands. Deleting one is unrecoverable — the chunk row
	// arrives with qdrant_point_id set, so the pipeline considers it embedded
	// and never re-embeds it, leaving a chunk that vector search cannot reach.
	//
	// Candidates are therefore re-checked against Postgres after Grace, and only
	// points still unreferenced on the second look are deleted. Anything
	// mid-index commits within seconds and is spared.
	Grace time.Duration
	// Batch is the scan/delete page size.
	Batch int
	// DryRun reports what would be deleted without deleting it.
	DryRun bool
	// Sleep waits out Grace. Defaults to time.Sleep; tests inject their own so
	// they need not wait in real time.
	Sleep func(context.Context, time.Duration) error
	// Logger receives progress. Defaults to the context logger.
	Logger *zap.Logger
}

const (
	defaultGrace = 5 * time.Minute
	defaultBatch = 1024
)

func (opts *Options) setDefaults(ctx context.Context) {
	if opts.Grace == 0 {
		opts.Grace = defaultGrace
	}
	if opts.Batch <= 0 {
		opts.Batch = defaultBatch
	}
	if opts.Sleep == nil {
		opts.Sleep = sleepCtx
	}
	if opts.Logger == nil {
		opts.Logger = zctx.From(ctx)
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Report summarizes a collection run.
type Report struct {
	// Scanned is how many points the vector store held.
	Scanned int
	// Candidates is how many looked orphaned on the first pass.
	Candidates int
	// Spared is how many candidates a chunk claimed during Grace, i.e. were
	// mid-index rather than orphaned.
	Spared int
	// Deleted is how many points were removed (0 when DryRun).
	Deleted int
	// DryRun echoes whether deletion was suppressed.
	DryRun bool
}

// Collector reclaims unreferenced points.
type Collector struct {
	points PointStore
	refs   RefStore
	opts   Options
}

// New builds a Collector. Both stores are required.
func New(points PointStore, refs RefStore, opts Options) (*Collector, error) {
	if points == nil {
		return nil, errors.New("vectorgc: point store required")
	}
	if refs == nil {
		return nil, errors.New("vectorgc: ref store required")
	}
	return &Collector{points: points, refs: refs, opts: opts}, nil
}

// Run scans the vector store and deletes points no chunk references.
//
// It deliberately reads Postgres twice, separated by Options.Grace: the first
// pass narrows a whole collection down to candidates, the second confirms each
// one is still unreferenced before it is deleted. See Options.Grace for why a
// single pass is unsafe.
func (c *Collector) Run(ctx context.Context) (Report, error) {
	c.opts.setDefaults(ctx)
	lg := c.opts.Logger
	rep := Report{DryRun: c.opts.DryRun}

	candidates, scanned, err := c.scan(ctx)
	if err != nil {
		return rep, err
	}
	rep.Scanned = scanned
	rep.Candidates = len(candidates)

	lg.Info("vector gc scan complete",
		zap.Int("scanned", rep.Scanned),
		zap.Int("candidates", rep.Candidates),
	)
	if len(candidates) == 0 {
		return rep, nil
	}

	// Wait out the pipeline's embed-then-persist window before trusting the
	// first pass, then re-check: anything mid-index will have committed by now.
	if err := c.opts.Sleep(ctx, c.opts.Grace); err != nil {
		return rep, errors.Wrap(err, "wait out grace")
	}

	orphans, err := c.confirm(ctx, candidates)
	if err != nil {
		return rep, err
	}
	rep.Spared = len(candidates) - len(orphans)
	if rep.Spared > 0 {
		lg.Info("vector gc spared points claimed during grace",
			zap.Int("spared", rep.Spared),
		)
	}
	if len(orphans) == 0 {
		return rep, nil
	}

	if c.opts.DryRun {
		lg.Info("vector gc dry run, not deleting",
			zap.Int("orphans", len(orphans)),
		)
		return rep, nil
	}

	deleted, err := c.deleteAll(ctx, orphans)
	rep.Deleted = deleted
	if err != nil {
		return rep, err
	}
	lg.Info("vector gc complete",
		zap.Int("scanned", rep.Scanned),
		zap.Int("deleted", rep.Deleted),
		zap.Int("spared", rep.Spared),
	)
	return rep, nil
}

// scan walks the collection and returns the points no chunk references.
func (c *Collector) scan(ctx context.Context) (candidates []uuid.UUID, scanned int, err error) {
	err = c.points.ScanPointIDs(ctx, c.opts.Batch, func(ids []uuid.UUID) error {
		scanned += len(ids)
		unref, err := c.unreferenced(ctx, ids)
		if err != nil {
			return err
		}
		candidates = append(candidates, unref...)
		return nil
	})
	if err != nil {
		return nil, scanned, errors.Wrap(err, "scan points")
	}
	return candidates, scanned, nil
}

// confirm re-checks candidates against Postgres, returning those still
// unreferenced.
func (c *Collector) confirm(ctx context.Context, candidates []uuid.UUID) ([]uuid.UUID, error) {
	var orphans []uuid.UUID
	for chunkIDs := range batches(candidates, c.opts.Batch) {
		unref, err := c.unreferenced(ctx, chunkIDs)
		if err != nil {
			return nil, err
		}
		orphans = append(orphans, unref...)
	}
	return orphans, nil
}

// unreferenced returns the subset of ids no chunk points at.
func (c *Collector) unreferenced(ctx context.Context, ids []uuid.UUID) ([]uuid.UUID, error) {
	referenced, err := c.refs.ReferencedPoints(ctx, ids)
	if err != nil {
		return nil, errors.Wrap(err, "look up referenced points")
	}
	var out []uuid.UUID
	for _, id := range ids {
		if !referenced[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

// deleteAll removes orphans in batches, returning how many were deleted.
func (c *Collector) deleteAll(ctx context.Context, orphans []uuid.UUID) (int, error) {
	var deleted int
	for batch := range batches(orphans, c.opts.Batch) {
		if err := c.points.Delete(ctx, batch); err != nil {
			// Report what did land: the caller logs it, and a partial sweep is
			// still progress. The next run re-finds whatever remains.
			return deleted, errors.Wrap(err, "delete orphaned points")
		}
		deleted += len(batch)
	}
	return deleted, nil
}

// batches yields successive slices of at most n elements.
func batches[T any](s []T, n int) func(func([]T) bool) {
	return func(yield func([]T) bool) {
		for i := 0; i < len(s); i += n {
			if !yield(s[i:min(i+n, len(s))]) {
				return
			}
		}
	}
}
