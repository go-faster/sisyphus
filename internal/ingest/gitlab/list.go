package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"strconv"

	"github.com/go-faster/errors"
)

// maxListPages bounds a full listing, so a paginator that never reports its
// last page cannot spin forever against an API that keeps answering.
const maxListPages = 1000

// ListSourceIDs returns the source_id of every issue, merge request or release
// currently present in project, for reconciliation against what is indexed.
//
// It is deliberately separate from the Fetch* methods. Those exist to build
// documents and pull discussions, links and system notes per item — many
// requests per object, which is the right trade for the handful of items an
// incremental run sees and completely wrong for a full listing. This reads one
// page at a time and keeps only the identifier.
//
// The returned IDs match what internal/chunk/gitlab writes as Document.SourceID
// (`<project>/issues/<iid>` and so on). They must keep matching: a reconcile
// compares these strings against indexed ones and deletes what it cannot find.
func (f *Fetcher) ListSourceIDs(ctx context.Context, project string, kind ResourceKind) ([]string, error) {
	var (
		path   string
		idOf   func(listItem) string
		params = url.Values{}
	)

	switch kind {
	case ResourceIssues:
		path = fmt.Sprintf("/api/v4/projects/%s/issues", encodeProjectRef(project))
		// state=all is the API's default today, but a reconcile that silently
		// listed only open issues would delete every closed one. Say it.
		params.Set("state", "all")
		idOf = func(it listItem) string { return fmt.Sprintf("%s/issues/%d", project, it.IID) }
	case ResourceMergeRequests:
		path = fmt.Sprintf("/api/v4/projects/%s/merge_requests", encodeProjectRef(project))
		params.Set("state", "all")
		idOf = func(it listItem) string { return fmt.Sprintf("%s/merge_requests/%d", project, it.IID) }
	case ResourceReleases:
		path = fmt.Sprintf("/api/v4/projects/%s/releases", encodeProjectRef(project))
		idOf = func(it listItem) string { return fmt.Sprintf("%s/releases/%s", project, it.TagName) }
	default:
		return nil, errors.Errorf("gitlab: unknown resource kind %q", kind)
	}

	var out []string
	for page := 1; page <= maxListPages; page++ {
		q := url.Values{}
		maps.Copy(q, params)
		q.Set("page", strconv.Itoa(page))
		q.Set("per_page", strconv.Itoa(f.pageSize))
		// Ordering by id keeps the walk stable while items are being updated
		// underneath it: with the default updated_at ordering, an item edited
		// mid-listing moves between pages and can be missed — and a missed item
		// is one a reconcile would delete.
		if kind != ResourceReleases {
			q.Set("order_by", "id")
			q.Set("sort", "asc")
		}

		req, err := f.buildRequest(ctx, path, q)
		if err != nil {
			return nil, err
		}
		body, err := f.doRequest(req, "fetcher.ListSourceIDs")
		if err != nil {
			return nil, errors.Wrapf(err, "list %s page %d", kind, page)
		}

		var items []listItem
		if err := json.Unmarshal(body, &items); err != nil {
			return nil, errors.Wrapf(err, "parse %s list response", kind)
		}
		for _, it := range items {
			id := idOf(it)
			if id == "" {
				continue
			}
			out = append(out, id)
		}

		if len(items) < f.pageSize {
			return out, nil
		}
	}
	// Falling out of the loop means the API never showed a short page. Refusing
	// is the safe answer: a truncated listing looks exactly like a mass
	// deletion to whoever consumes it.
	return nil, errors.Errorf("gitlab: %s listing for %s exceeded %d pages", kind, project, maxListPages)
}

// ResourceKind names a listable GitLab resource.
type ResourceKind string

// Listable GitLab resources, one per index.Source this package feeds.
const (
	ResourceIssues        ResourceKind = "issues"
	ResourceMergeRequests ResourceKind = "merge_requests"
	ResourceReleases      ResourceKind = "releases"
)

// listItem is the identifying subset of a listed object. Everything else in the
// response is ignored: a listing exists to answer "does this still exist".
type listItem struct {
	IID     int    `json:"iid"`
	TagName string `json:"tag_name"`
}
