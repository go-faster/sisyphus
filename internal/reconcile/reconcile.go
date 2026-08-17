// Package reconcile deletes indexed documents whose upstream object is gone.
//
// Incremental ingestion is cursor-based (`updated_after`, `updated >= …`), and
// a deleted object never appears in such a window: it has no update, it simply
// stops existing. Everything a cursor run indexes therefore accumulates
// forever, and only a full listing can tell "not changed" from "not there".
//
// This is the only thing in the system that deletes indexed content on the
// evidence of an *absence*, so it is built to refuse rather than guess. Read
// [Options] before changing any of it: every guard there stands for a way a
// listing can be wrong.
package reconcile

import (
	"context"
	"strconv"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/index"
)

// Scope is one independently reconcilable slice of a source: a GitLab
// project's issues, a Jira project's issues.
//
// Scoping matters because sources are global. Every configured project's
// issues share the source `gitlab_issue`, distinguished only by a source_id
// prefix, so a reconcile that diffed a whole source against one project's
// listing would delete every other project's documents. IDPrefix is what keeps
// a scope's blast radius to the objects it actually listed — and what leaves
// the documents of a project someone removed from config untouched, rather
// than deleting a corpus because a line was commented out.
type Scope struct {
	// Source is the index.Source the documents live under.
	Source index.Source
	// Name identifies the scope in logs and reports (e.g. "group/repo").
	Name string
	// IDPrefix bounds which of the source's documents this scope owns. A
	// document whose SourceID lacks the prefix is never considered, and never
	// deleted.
	IDPrefix string
	// List returns every source_id currently present upstream in this scope.
	// It must return an error rather than a short list when it cannot see
	// everything: a truncated listing is read here as a mass deletion.
	List func(ctx context.Context) ([]string, error)
}

// Store is the index side of a reconcile.
type Store interface {
	// IndexedSourceIDs returns the source_ids indexed under source whose id
	// starts with prefix.
	IndexedSourceIDs(ctx context.Context, source index.Source, prefix string) ([]string, error)
	// DeleteDocuments removes the documents, their chunks and their vector
	// points.
	DeleteDocuments(ctx context.Context, source index.Source, sourceIDs []string) (chunks int, err error)
}

// Options configures a [Reconciler].
type Options struct {
	// MaxDeleteFraction refuses a scope whose diff would delete more than this
	// share of its indexed documents (0.2 = 20%).
	//
	// A listing that fails halfway, an account that lost access to a project,
	// a token scoped down, a project renamed upstream: each returns *fewer*
	// objects rather than an error, and each is indistinguishable from a mass
	// deletion. Real deletions are a trickle; a listing failure is a cliff. So
	// the cliff is refused and reported, and a genuine bulk deletion needs a
	// human to widen this or run --reset.
	MaxDeleteFraction float64
	// MinIndexedForFraction is how many indexed documents a scope needs before
	// MaxDeleteFraction applies. Below it, the fraction is meaningless — one
	// of three documents is 33% — and the scope reconciles unguarded.
	MinIndexedForFraction int
	// DryRun reports what would be deleted without deleting it.
	DryRun bool
	// Logger receives progress. Defaults to the context logger.
	Logger *zap.Logger
}

const (
	defaultMaxDeleteFraction     = 0.2
	defaultMinIndexedForFraction = 20
)

func (opts *Options) setDefaults(ctx context.Context) {
	if opts.MaxDeleteFraction == 0 {
		opts.MaxDeleteFraction = defaultMaxDeleteFraction
	}
	if opts.MinIndexedForFraction == 0 {
		opts.MinIndexedForFraction = defaultMinIndexedForFraction
	}
	if opts.Logger == nil {
		opts.Logger = zctx.From(ctx)
	}
}

// ScopeReport is the outcome of reconciling one scope.
type ScopeReport struct {
	Scope    string
	Source   index.Source
	Upstream int
	Indexed  int
	// Missing is how many indexed documents the listing did not contain.
	Missing int
	// Deleted is how many were actually removed (0 when DryRun or Refused).
	Deleted int
	Chunks  int
	// Refused reports that the diff tripped a guard and nothing was deleted.
	// The reason is in Reason.
	Refused bool
	Reason  string
	// Err is the scope's failure, if any. A failed scope deletes nothing.
	Err error
}

// Report summarizes a whole run.
type Report struct {
	Scopes []ScopeReport
	DryRun bool
}

// Deleted totals the documents removed across scopes.
func (r Report) Deleted() int {
	var n int
	for _, s := range r.Scopes {
		n += s.Deleted
	}
	return n
}

// Refused reports whether any scope tripped a guard.
func (r Report) Refused() bool {
	for _, s := range r.Scopes {
		if s.Refused {
			return true
		}
	}
	return false
}

// Reconciler diffs upstream listings against the index.
type Reconciler struct {
	store Store
	opts  Options
}

// New builds a Reconciler over store.
func New(store Store, opts Options) (*Reconciler, error) {
	if store == nil {
		return nil, errors.New("reconcile: store required")
	}
	return &Reconciler{store: store, opts: opts}, nil
}

// Run reconciles every scope, independently.
//
// A scope that fails or is refused does not stop the others: they are separate
// projects with separate listings, and one unreadable project must not park
// deletion detection for the rest. The error returned reports that something
// failed; the Report says what.
func (r *Reconciler) Run(ctx context.Context, scopes []Scope) (Report, error) {
	opts := r.opts
	opts.setDefaults(ctx)

	rep := Report{DryRun: opts.DryRun, Scopes: make([]ScopeReport, 0, len(scopes))}

	var failed error
	for _, sc := range scopes {
		sr := r.runScope(ctx, sc, opts)
		rep.Scopes = append(rep.Scopes, sr)
		if sr.Err != nil {
			failed = errors.Join(failed, errors.Wrapf(sr.Err, "reconcile %s", sc.Name))
		}
	}
	return rep, failed
}

func (r *Reconciler) runScope(ctx context.Context, sc Scope, opts Options) ScopeReport {
	sr := ScopeReport{Scope: sc.Name, Source: sc.Source}
	lg := opts.Logger.With(
		zap.String("scope", sc.Name),
		zap.String("source", string(sc.Source)),
	)

	start := time.Now()
	upstream, err := sc.List(ctx)
	if err != nil {
		// Deliberately fatal for the scope: a partial listing is exactly what
		// must never reach the diff.
		sr.Err = errors.Wrap(err, "list upstream")
		return sr
	}
	sr.Upstream = len(upstream)

	indexed, err := r.store.IndexedSourceIDs(ctx, sc.Source, sc.IDPrefix)
	if err != nil {
		sr.Err = errors.Wrap(err, "read indexed ids")
		return sr
	}
	sr.Indexed = len(indexed)

	present := make(map[string]struct{}, len(upstream))
	for _, id := range upstream {
		present[id] = struct{}{}
	}
	var missing []string
	for _, id := range indexed {
		if _, ok := present[id]; !ok {
			missing = append(missing, id)
		}
	}
	sr.Missing = len(missing)

	lg = lg.With(
		zap.Int("upstream", sr.Upstream),
		zap.Int("indexed", sr.Indexed),
		zap.Int("missing", sr.Missing),
		zap.Duration("listed_in", time.Since(start)),
	)

	if len(missing) == 0 {
		lg.Debug("reconcile: nothing to delete")
		return sr
	}

	if reason, refused := guard(sr, opts); refused {
		sr.Refused = true
		sr.Reason = reason
		// Loud, and not an error: the run did its job by declining. An
		// operator decides whether this is a real bulk deletion.
		lg.Error("reconcile refused: diff too large to be a deletion",
			zap.String("reason", reason),
			zap.Float64("max_delete_fraction", opts.MaxDeleteFraction))
		return sr
	}

	if opts.DryRun {
		lg.Info("reconcile: would delete documents missing upstream",
			zap.Strings("sample", sample(missing, 10)))
		return sr
	}

	chunks, err := r.store.DeleteDocuments(ctx, sc.Source, missing)
	if err != nil {
		sr.Err = errors.Wrap(err, "delete documents")
		return sr
	}
	sr.Deleted = len(missing)
	sr.Chunks = chunks
	lg.Info("reconcile: deleted documents missing upstream",
		zap.Int("documents", sr.Deleted),
		zap.Int("chunks", sr.Chunks),
		zap.Strings("sample", sample(missing, 10)))
	return sr
}

// guard decides whether a diff is too large to believe.
func guard(sr ScopeReport, opts Options) (reason string, refused bool) {
	// An empty listing against a non-empty index is the signature of lost
	// access or a renamed project, never of someone deleting every issue.
	// Refuse it regardless of size: the fraction check below would let it
	// through whenever the index is small.
	if sr.Upstream == 0 && sr.Indexed > 0 {
		return "upstream listing was empty while documents are indexed", true
	}
	if sr.Indexed < opts.MinIndexedForFraction {
		return "", false
	}
	if frac := float64(sr.Missing) / float64(sr.Indexed); frac > opts.MaxDeleteFraction {
		return "would delete " + formatPercent(frac) + " of indexed documents", true
	}
	return "", false
}

func formatPercent(f float64) string {
	return strconv.FormatFloat(f*100, 'f', 1, 64) + "%"
}

// sample returns at most n ids, for a log line that shows what is going rather
// than only how much.
func sample(ids []string, n int) []string {
	if len(ids) <= n {
		return ids
	}
	return ids[:n]
}
