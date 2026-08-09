package reconcile

import (
	"context"
	"testing"

	"github.com/go-faster/errors"
	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/index"
)

// fakeStore records what a reconcile asked for and what it deleted.
type fakeStore struct {
	indexed map[string][]string // keyed by "<source>|<prefix>"
	deleted map[string][]string // keyed by source
	err     error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		indexed: map[string][]string{},
		deleted: map[string][]string{},
	}
}

func (s *fakeStore) IndexedSourceIDs(_ context.Context, source index.Source, prefix string) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.indexed[string(source)+"|"+prefix], nil
}

func (s *fakeStore) DeleteDocuments(_ context.Context, source index.Source, ids []string) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	s.deleted[string(source)] = append(s.deleted[string(source)], ids...)
	return len(ids), nil
}

func listing(ids ...string) func(context.Context) ([]string, error) {
	return func(context.Context) ([]string, error) { return ids, nil }
}

func ids(prefix string, from, to int) []string {
	var out []string
	for i := from; i <= to; i++ {
		out = append(out, prefix+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	return out
}

func TestReconcileDeletesOnlyWhatIsMissing(t *testing.T) {
	store := newFakeStore()
	store.indexed["gitlab_issue|p/issues/"] = []string{"p/issues/1", "p/issues/2", "p/issues/3"}

	r, err := New(store, Options{MinIndexedForFraction: 1000})
	require.NoError(t, err)

	rep, err := r.Run(t.Context(), []Scope{{
		Source:   index.SourceGitLabIssue,
		Name:     "p issues",
		IDPrefix: "p/issues/",
		List:     listing("p/issues/1", "p/issues/3"),
	}})
	require.NoError(t, err)
	require.Equal(t, 1, rep.Deleted())
	require.Equal(t, []string{"p/issues/2"}, store.deleted["gitlab_issue"])
}

// TestReconcileRefusesEmptyListing is the guard that matters most: an empty
// listing against a non-empty index is lost access or a renamed project, never
// someone deleting every issue.
func TestReconcileRefusesEmptyListing(t *testing.T) {
	store := newFakeStore()
	store.indexed["jira|ABC-"] = []string{"ABC-1", "ABC-2"}

	r, err := New(store, Options{})
	require.NoError(t, err)

	rep, err := r.Run(t.Context(), []Scope{{
		Source:   index.SourceJira,
		Name:     "ABC",
		IDPrefix: "ABC-",
		List:     listing(),
	}})
	require.NoError(t, err, "refusing is not a run failure")
	require.True(t, rep.Refused())
	require.Zero(t, rep.Deleted())
	require.Empty(t, store.deleted, "nothing may be deleted on an empty listing")
}

// TestReconcileRefusesLargeDiff pins the cliff-vs-trickle rule: real deletions
// dribble in, a half-failed listing arrives as a cliff.
func TestReconcileRefusesLargeDiff(t *testing.T) {
	indexed := ids("ABC-", 1, 100)
	store := newFakeStore()
	store.indexed["jira|ABC-"] = indexed

	r, err := New(store, Options{MaxDeleteFraction: 0.2, MinIndexedForFraction: 20})
	require.NoError(t, err)

	// Half the project vanished from the listing.
	rep, err := r.Run(t.Context(), []Scope{{
		Source:   index.SourceJira,
		Name:     "ABC",
		IDPrefix: "ABC-",
		List:     listing(indexed[:50]...),
	}})
	require.NoError(t, err)
	require.True(t, rep.Refused())
	require.Zero(t, rep.Deleted())
	require.Contains(t, rep.Scopes[0].Reason, "%")
}

// TestReconcileAllowsSmallDiff is the other half: a genuine deletion or two
// must actually go through.
func TestReconcileAllowsSmallDiff(t *testing.T) {
	indexed := ids("ABC-", 1, 100)
	store := newFakeStore()
	store.indexed["jira|ABC-"] = indexed

	r, err := New(store, Options{MaxDeleteFraction: 0.2, MinIndexedForFraction: 20})
	require.NoError(t, err)

	rep, err := r.Run(t.Context(), []Scope{{
		Source:   index.SourceJira,
		Name:     "ABC",
		IDPrefix: "ABC-",
		List:     listing(indexed[:98]...),
	}})
	require.NoError(t, err)
	require.False(t, rep.Refused())
	require.Equal(t, 2, rep.Deleted())
}

// TestReconcileSmallScopeSkipsFractionGuard pins that the fraction does not
// apply below MinIndexedForFraction, where one of three documents is already a
// third and the guard would block every real deletion.
func TestReconcileSmallScopeSkipsFractionGuard(t *testing.T) {
	store := newFakeStore()
	store.indexed["jira|ABC-"] = []string{"ABC-1", "ABC-2", "ABC-3"}

	r, err := New(store, Options{MaxDeleteFraction: 0.2, MinIndexedForFraction: 20})
	require.NoError(t, err)

	rep, err := r.Run(t.Context(), []Scope{{
		Source:   index.SourceJira,
		Name:     "ABC",
		IDPrefix: "ABC-",
		List:     listing("ABC-1", "ABC-2"),
	}})
	require.NoError(t, err)
	require.False(t, rep.Refused())
	require.Equal(t, 1, rep.Deleted())
}

// TestReconcileListErrorDeletesNothing pins that a listing failure is fatal to
// its scope: a partial listing is exactly what must never reach the diff.
func TestReconcileListErrorDeletesNothing(t *testing.T) {
	store := newFakeStore()
	store.indexed["jira|ABC-"] = []string{"ABC-1", "ABC-2"}

	r, err := New(store, Options{})
	require.NoError(t, err)

	boom := errors.New("upstream exploded")
	rep, err := r.Run(t.Context(), []Scope{{
		Source:   index.SourceJira,
		Name:     "ABC",
		IDPrefix: "ABC-",
		List:     func(context.Context) ([]string, error) { return nil, boom },
	}})
	require.ErrorIs(t, err, boom)
	require.Zero(t, rep.Deleted())
	require.Empty(t, store.deleted)
}

// TestReconcileScopesAreIndependent pins that one unreadable project does not
// park deletion detection for the others.
func TestReconcileScopesAreIndependent(t *testing.T) {
	store := newFakeStore()
	store.indexed["jira|ABC-"] = []string{"ABC-1", "ABC-2"}
	store.indexed["jira|XYZ-"] = []string{"XYZ-1", "XYZ-2"}

	r, err := New(store, Options{MinIndexedForFraction: 1000})
	require.NoError(t, err)

	rep, err := r.Run(t.Context(), []Scope{
		{
			Source: index.SourceJira, Name: "ABC", IDPrefix: "ABC-",
			List: func(context.Context) ([]string, error) { return nil, errors.New("nope") },
		},
		{
			Source: index.SourceJira, Name: "XYZ", IDPrefix: "XYZ-",
			List: listing("XYZ-1"),
		},
	})
	require.Error(t, err)
	require.Equal(t, 1, rep.Deleted(), "the healthy scope still reconciles")
	require.Equal(t, []string{"XYZ-2"}, store.deleted["jira"])
}

// TestReconcileDryRunDeletesNothing pins the flag operators are told to use
// first.
func TestReconcileDryRunDeletesNothing(t *testing.T) {
	store := newFakeStore()
	store.indexed["jira|ABC-"] = []string{"ABC-1", "ABC-2"}

	r, err := New(store, Options{DryRun: true, MinIndexedForFraction: 1000})
	require.NoError(t, err)

	rep, err := r.Run(t.Context(), []Scope{{
		Source: index.SourceJira, Name: "ABC", IDPrefix: "ABC-",
		List: listing("ABC-1"),
	}})
	require.NoError(t, err)
	require.Equal(t, 1, rep.Scopes[0].Missing, "a dry run still reports what it found")
	require.Zero(t, rep.Deleted())
	require.Empty(t, store.deleted)
}
