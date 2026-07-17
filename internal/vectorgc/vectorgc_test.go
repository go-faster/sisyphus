package vectorgc

import (
	"context"
	"testing"
	"time"

	"github.com/go-faster/errors"
	"github.com/google/uuid"
)

// fakePoints is an in-memory vector store.
type fakePoints struct {
	ids       []uuid.UUID
	deleted   []uuid.UUID
	deleteErr error
	scanErr   error
	// onScan runs after the scan completes, letting a test simulate a write
	// that races the collector.
	onScan func()
}

func (f *fakePoints) ScanPointIDs(_ context.Context, batch int, fn func([]uuid.UUID) error) error {
	if f.scanErr != nil {
		return f.scanErr
	}
	for i := 0; i < len(f.ids); i += batch {
		if err := fn(f.ids[i:min(i+batch, len(f.ids))]); err != nil {
			return err
		}
	}
	if f.onScan != nil {
		f.onScan()
	}
	return nil
}

func (f *fakePoints) Delete(_ context.Context, ids []uuid.UUID) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, ids...)
	return nil
}

// fakeRefs is an in-memory stand-in for the chunks table.
type fakeRefs struct {
	live map[uuid.UUID]bool
	err  error
}

func (f *fakeRefs) ReferencedPoints(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[uuid.UUID]bool{}
	for _, id := range ids {
		if f.live[id] {
			out[id] = true
		}
	}
	return out, nil
}

// noSleep skips the grace wait so tests never depend on real time.
func noSleep(_ context.Context, _ time.Duration) error { return nil }

func TestRunDeletesOnlyUnreferencedPoints(t *testing.T) {
	live, orphan := uuid.New(), uuid.New()
	points := &fakePoints{ids: []uuid.UUID{live, orphan}}
	refs := &fakeRefs{live: map[uuid.UUID]bool{live: true}}

	c, err := New(points, refs, Options{Sleep: noSleep})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := c.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Scanned != 2 || rep.Candidates != 1 || rep.Deleted != 1 {
		t.Fatalf("report = %+v, want scanned 2, candidates 1, deleted 1", rep)
	}
	if len(points.deleted) != 1 || points.deleted[0] != orphan {
		t.Fatalf("deleted = %v, want only the orphan %v", points.deleted, orphan)
	}
}

// TestRunSparesPointClaimedDuringGrace is the safety property this package
// exists for. The pipeline upserts a point before committing its chunk row, so a
// document mid-index looks exactly like an orphan. Deleting one is
// unrecoverable: the row lands with qdrant_point_id set, so the pipeline treats
// the chunk as embedded and never re-embeds it, and vector search can never
// reach it again.
func TestRunSparesPointClaimedDuringGrace(t *testing.T) {
	inFlight := uuid.New()
	refs := &fakeRefs{live: map[uuid.UUID]bool{}}
	points := &fakePoints{
		ids: []uuid.UUID{inFlight},
		// The chunk row commits after the scan saw the point but before the
		// confirm pass -- exactly the window Grace covers.
		onScan: func() { refs.live[inFlight] = true },
	}

	c, err := New(points, refs, Options{Sleep: noSleep})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := c.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Candidates != 1 {
		t.Fatalf("candidates = %d, want 1 (it looked orphaned on the first pass)", rep.Candidates)
	}
	if rep.Spared != 1 {
		t.Fatalf("spared = %d, want 1", rep.Spared)
	}
	if rep.Deleted != 0 || len(points.deleted) != 0 {
		t.Fatalf("deleted %v, want none: an in-flight point must survive", points.deleted)
	}
}

func TestRunDryRunDeletesNothing(t *testing.T) {
	orphan := uuid.New()
	points := &fakePoints{ids: []uuid.UUID{orphan}}
	refs := &fakeRefs{live: map[uuid.UUID]bool{}}

	c, err := New(points, refs, Options{Sleep: noSleep, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := c.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.DryRun {
		t.Error("report should echo DryRun")
	}
	if rep.Candidates != 1 {
		t.Fatalf("candidates = %d, want 1: a dry run still reports what it found", rep.Candidates)
	}
	if rep.Deleted != 0 || len(points.deleted) != 0 {
		t.Fatalf("dry run deleted %v, want none", points.deleted)
	}
}

func TestRunNoOrphansSkipsGraceAndDelete(t *testing.T) {
	live := uuid.New()
	points := &fakePoints{ids: []uuid.UUID{live}}
	refs := &fakeRefs{live: map[uuid.UUID]bool{live: true}}

	var slept bool
	c, err := New(points, refs, Options{Sleep: func(context.Context, time.Duration) error {
		slept = true
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := c.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Candidates != 0 || rep.Deleted != 0 {
		t.Fatalf("report = %+v, want nothing to do", rep)
	}
	if slept {
		t.Error("should not wait out grace when there is nothing to collect")
	}
}

func TestRunBatchesScanAndDelete(t *testing.T) {
	var ids []uuid.UUID
	for range 5 {
		ids = append(ids, uuid.New())
	}
	points := &fakePoints{ids: ids}
	refs := &fakeRefs{live: map[uuid.UUID]bool{}}

	c, err := New(points, refs, Options{Sleep: noSleep, Batch: 2})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := c.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Scanned != 5 {
		t.Fatalf("scanned = %d, want 5 across batches", rep.Scanned)
	}
	if rep.Deleted != 5 || len(points.deleted) != 5 {
		t.Fatalf("deleted = %d, want all 5 across batches", rep.Deleted)
	}
}

func TestRunReportsPartialDeleteOnError(t *testing.T) {
	orphan := uuid.New()
	points := &fakePoints{ids: []uuid.UUID{orphan}, deleteErr: errors.New("qdrant down")}
	refs := &fakeRefs{live: map[uuid.UUID]bool{}}

	c, err := New(points, refs, Options{Sleep: noSleep})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := c.Run(t.Context())
	if err == nil {
		t.Fatal("expected the delete failure to surface")
	}
	if rep.Deleted != 0 {
		t.Fatalf("deleted = %d, want 0 when the delete failed", rep.Deleted)
	}
}

func TestRunSurfacesRefStoreError(t *testing.T) {
	points := &fakePoints{ids: []uuid.UUID{uuid.New()}}
	refs := &fakeRefs{err: errors.New("postgres down")}

	c, err := New(points, refs, Options{Sleep: noSleep})
	if err != nil {
		t.Fatal(err)
	}
	// Postgres being unreachable must never read as "nothing is referenced",
	// which would delete the entire collection.
	if _, err := c.Run(t.Context()); err == nil {
		t.Fatal("expected the ref lookup failure to surface, not an empty ref set")
	}
	if len(points.deleted) != 0 {
		t.Fatalf("deleted %v while Postgres was unreachable", points.deleted)
	}
}

func TestRunAbortsWhenGraceIsCancelled(t *testing.T) {
	points := &fakePoints{ids: []uuid.UUID{uuid.New()}}
	refs := &fakeRefs{live: map[uuid.UUID]bool{}}

	c, err := New(points, refs, Options{Sleep: func(context.Context, time.Duration) error {
		return context.Canceled
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Run(t.Context()); err == nil {
		t.Fatal("expected cancellation during grace to abort the run")
	}
	if len(points.deleted) != 0 {
		t.Fatalf("deleted %v after an aborted grace", points.deleted)
	}
}

func TestNewRequiresStores(t *testing.T) {
	if _, err := New(nil, &fakeRefs{}, Options{}); err == nil {
		t.Error("expected error without a point store")
	}
	if _, err := New(&fakePoints{}, nil, Options{}); err == nil {
		t.Error("expected error without a ref store")
	}
}

func TestDefaultGraceIsNonZero(t *testing.T) {
	// A zero grace would collapse the two passes into one and reintroduce the
	// race, so the default must not be zero.
	var opts Options
	opts.setDefaults(t.Context())
	if opts.Grace <= 0 {
		t.Fatalf("default grace = %s, want > 0", opts.Grace)
	}
}
