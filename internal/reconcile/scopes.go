package reconcile

import (
	"context"

	"github.com/go-faster/sisyphus/internal/index"
	gitlabingest "github.com/go-faster/sisyphus/internal/ingest/gitlab"
	jiraingest "github.com/go-faster/sisyphus/internal/ingest/jira"
)

// GitLabLister is the listing half of internal/ingest/gitlab.
type GitLabLister interface {
	ListSourceIDs(ctx context.Context, project string, kind gitlabingest.ResourceKind) ([]string, error)
}

// JiraLister is the listing half of internal/ingest/jira.
type JiraLister interface {
	ListSourceIDs(ctx context.Context, project string) ([]string, error)
}

// gitlabResource pairs a listable resource with the index.Source its documents
// are stored under, and whether ingestion indexes it at all.
type gitlabResource struct {
	kind    gitlabingest.ResourceKind
	source  index.Source
	enabled bool
}

// GitLabScopes builds one scope per (project, resource) pair that ingestion is
// configured to index.
//
// One scope per pair, not one per source: an unreadable project must only
// disable its own deletion detection, and — more importantly — a project that
// is no longer configured has no scope at all, so its documents are never
// diffed against anything and never deleted. Dropping a project from config
// stops updating its documents; it does not erase them.
func GitLabScopes(lister GitLabLister, projects []string, issues, mrs, releases bool) []Scope {
	kinds := []gitlabResource{
		{gitlabingest.ResourceIssues, index.SourceGitLabIssue, issues},
		{gitlabingest.ResourceMergeRequests, index.SourceGitLabMR, mrs},
		{gitlabingest.ResourceReleases, index.SourceGitLabRelease, releases},
	}

	var scopes []Scope
	for _, project := range projects {
		for _, k := range kinds {
			if !k.enabled {
				continue
			}
			scopes = append(scopes, Scope{
				Source: k.source,
				Name:   project + " " + string(k.kind),
				// Documents are keyed "<project>/<resource>/<id>", so the
				// prefix owns exactly this project's objects of this kind.
				IDPrefix: project + "/" + string(k.kind) + "/",
				List: func(ctx context.Context) ([]string, error) {
					return lister.ListSourceIDs(ctx, project, k.kind)
				},
			})
		}
	}
	return scopes
}

// JiraScopes builds one scope per configured Jira project.
//
// A Jira document's source_id is its issue key ("ABC-123"), so the project's
// key plus a dash is the prefix that owns it.
func JiraScopes(lister JiraLister, projects []string) []Scope {
	scopes := make([]Scope, 0, len(projects))
	for _, project := range projects {
		scopes = append(scopes, Scope{
			Source:   index.SourceJira,
			Name:     project,
			IDPrefix: project + "-",
			List: func(ctx context.Context) ([]string, error) {
				return lister.ListSourceIDs(ctx, project)
			},
		})
	}
	return scopes
}

var (
	_ GitLabLister = (*gitlabingest.Fetcher)(nil)
	_ JiraLister   = (*jiraingest.Fetcher)(nil)
)
