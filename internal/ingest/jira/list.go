package jira

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"go.uber.org/zap"
)

// maxListPages bounds a full listing, so a paginator that never reports its
// last page cannot spin forever against an API that keeps answering.
const maxListPages = 1000

// ListSourceIDs returns the key of every issue currently visible in project,
// for reconciliation against what is indexed.
//
// It asks for `fields=key` rather than the `*all` a real fetch uses: a listing
// answers "does this still exist", and pulling every field plus the changelog
// for a whole project to learn that would cost orders of magnitude more.
//
// A key is exactly what internal/chunk/jira writes as Document.SourceID, and a
// reconcile deletes indexed documents whose key is absent here — so an issue
// this cannot see is an issue that gets deleted. Jira only returns what the
// authenticated account may read, which is why a reconcile refuses to act on a
// listing that came back suspiciously small.
func (f *Fetcher) ListSourceIDs(ctx context.Context, project string) ([]string, error) {
	if strings.TrimSpace(project) == "" {
		return nil, errors.New("jira: empty project")
	}

	// No `updated` bound and no ORDER BY updated: this is the whole project,
	// ordered by key so the walk is stable while issues are edited underneath
	// it. An issue that moves between pages mid-listing is one a reconcile
	// would otherwise delete.
	jql := `project = "` + project + `" ORDER BY key ASC`

	var (
		out     []string
		startAt int
	)
	for page := range maxListPages {
		req, err := f.buildRequest(ctx, jql, startAt, f.pageSize, "key")
		if err != nil {
			return nil, errors.Wrap(err, "build request")
		}

		body, err := f.doRequest(req, "jira list")
		if err != nil {
			return nil, errors.Wrapf(err, "list %s page %d", project, page)
		}

		var resp jiraSearchResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, errors.Wrap(err, "parse list response")
		}
		for _, iss := range resp.Issues {
			if iss.Key == "" {
				continue
			}
			out = append(out, iss.Key)
		}

		if len(resp.Issues) < f.pageSize {
			zctx.From(ctx).Debug("jira listing complete",
				zap.String("project", project), zap.Int("issues", len(out)))
			return out, nil
		}
		startAt += len(resp.Issues)
	}
	// A listing that never showed a short page is truncated, and a truncated
	// listing is indistinguishable from a mass deletion downstream.
	return nil, errors.Errorf("jira: listing for %s exceeded %d pages", project, maxListPages)
}
