package reconcile

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/index"
	gitlabingest "github.com/go-faster/sisyphus/internal/ingest/gitlab"
)

type fakeGitLabLister struct {
	byProject map[string][]string
	asked     []string
}

func (f *fakeGitLabLister) ListSourceIDs(_ context.Context, project string, kind gitlabingest.ResourceKind) ([]string, error) {
	f.asked = append(f.asked, project+"/"+string(kind))
	return f.byProject[project+"/"+string(kind)], nil
}

type fakeJiraLister struct{ byProject map[string][]string }

func (f *fakeJiraLister) ListSourceIDs(_ context.Context, project string) ([]string, error) {
	return f.byProject[project], nil
}

func TestGitLabScopesCoverEnabledResourcesOnly(t *testing.T) {
	scopes := GitLabScopes(&fakeGitLabLister{}, []string{"grp/one"}, true, false, false)
	require.Len(t, scopes, 1)
	require.Equal(t, index.SourceGitLabIssue, scopes[0].Source)
	require.Equal(t, "grp/one/issues/", scopes[0].IDPrefix)
}

func TestGitLabScopePrefixMatchesDocumentSourceID(t *testing.T) {
	scopes := GitLabScopes(&fakeGitLabLister{}, []string{"grp/one"}, true, true, true)
	require.Len(t, scopes, 3)

	// These prefixes must stay in step with internal/chunk/gitlab's
	// "<project>/issues/<iid>" style source ids, or a reconcile either sees no
	// documents at all or diffs the wrong ones.
	byPrefix := map[string]index.Source{}
	for _, s := range scopes {
		byPrefix[s.IDPrefix] = s.Source
	}
	require.Equal(t, index.SourceGitLabIssue, byPrefix["grp/one/issues/"])
	require.Equal(t, index.SourceGitLabMR, byPrefix["grp/one/merge_requests/"])
	require.Equal(t, index.SourceGitLabRelease, byPrefix["grp/one/releases/"])
}

// TestScopesBindTheirOwnProject pins that each scope's List closure captures
// its own project. A loop variable shared across closures would make every
// scope list the last project and delete the rest.
func TestScopesBindTheirOwnProject(t *testing.T) {
	lister := &fakeGitLabLister{byProject: map[string][]string{
		"grp/one/issues": {"grp/one/issues/1"},
		"grp/two/issues": {"grp/two/issues/1"},
	}}
	scopes := GitLabScopes(lister, []string{"grp/one", "grp/two"}, true, false, false)
	require.Len(t, scopes, 2)

	for _, s := range scopes {
		got, err := s.List(t.Context())
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Contains(t, got[0], s.IDPrefix)
	}
	require.Equal(t, []string{"grp/one/issues", "grp/two/issues"}, lister.asked)
}

// TestUnconfiguredProjectGetsNoScope is the property that keeps a config edit
// from erasing a corpus: sources are global across projects, so the only thing
// standing between "removed grp/two from config" and "deleted every grp/two
// document" is that no scope claims its id prefix.
func TestUnconfiguredProjectGetsNoScope(t *testing.T) {
	scopes := GitLabScopes(&fakeGitLabLister{}, []string{"grp/one"}, true, true, true)
	for _, s := range scopes {
		require.NotContains(t, s.IDPrefix, "grp/two")
	}

	jira := JiraScopes(&fakeJiraLister{}, []string{"ABC"})
	require.Len(t, jira, 1)
	require.Equal(t, "ABC-", jira[0].IDPrefix)
}

func TestJiraScopePrefixIsProjectKey(t *testing.T) {
	scopes := JiraScopes(&fakeJiraLister{}, []string{"ABC", "XYZ"})
	require.Len(t, scopes, 2)
	require.Equal(t, index.SourceJira, scopes[0].Source)
	require.Equal(t, "ABC-", scopes[0].IDPrefix)
	require.Equal(t, "XYZ-", scopes[1].IDPrefix)
}
