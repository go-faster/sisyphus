package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/go-faster/errors"

	chunkgitlab "github.com/go-faster/sisyphus/internal/chunk/gitlab"
)

// The conflict sweep is a second, cursorless read of the same API, and it
// exists because the incremental one cannot see this event at all: a merge
// request usually starts conflicting when somebody else merges into its target
// branch, which does not touch the MR's own updated_at. Nothing brings it back
// into an updated_after window, so the author would learn about the conflict
// only the next time they happened to push.
//
// Two GitLab specifics shape it. Mergeability is computed asynchronously and
// list endpoints do not recompute it — "Listing merge requests might not
// proactively update merge_status (which also affects has_conflicts), as this
// can be an expensive operation" — so the sweep asks for a recheck and treats
// an unresolved verdict as "not conflicting" rather than guessing. And it is
// standing state with no timestamp of its own: nothing says when the conflict
// appeared, which is why the notification dedups on the head SHA instead (see
// internal/notify/gitlab.projectConflict).

// detailedStatusConflict is the detailed_merge_status value naming a conflict
// with the target branch. The other blocked statuses (a failing pipeline, a
// missing approval) are not this event: nobody needs to rebase for them.
const detailedStatusConflict = "conflict"

// conflictMaxPages bounds one project's sweep. A backlog larger than this is a
// misconfigured lookback, not a working sweep, and paging it forever would put
// the daemon in a loop against the API instead of on the next tick.
const conflictMaxPages = 20

// ConflictRef pairs a conflicting merge request with its project, since one
// sweep spans every configured project.
type ConflictRef struct {
	Project string
	MR      ConflictMR
}

// ConflictMR is a merge request that cannot be merged into its target branch,
// carrying only what the notification needs: who to tell (author, assignees),
// what to say (title, branches), and the head SHA the notification dedups on.
//
// It is deliberately not a [chunkgitlab.MergeRequest]: the sweep skips the
// per-MR discussions and system-notes fetches the ingestion run does, which is
// what keeps it cheap enough to run on its own ticker.
type ConflictMR struct {
	IID          int
	Title        string
	WebURL       string
	SourceBranch string
	TargetBranch string
	// SHA is the head commit of the source branch. It is the dedup basis: the
	// same conflict re-observed carries the same SHA, and a push that fails to
	// resolve it carries a new one, which is news again.
	SHA       string
	Author    chunkgitlab.User
	Assignees []string
	Updated   time.Time
	// Status is detailed_merge_status as GitLab reported it, kept for logs.
	Status string
}

// FetchConflicts sweeps every configured project for open merge requests that
// currently conflict with their target branch, ignoring the ingestion cursor.
//
// since bounds the sweep to merge requests touched recently: an MR nobody has
// pushed to in months is not news, and the bound is also what keeps the first
// sweep after this shipped from announcing the whole backlog. A zero since
// sweeps every open MR.
func (f *Fetcher) FetchConflicts(ctx context.Context, since time.Time) ([]ConflictRef, error) {
	projects := projectRefs(f.projects)
	if len(projects) == 0 {
		return nil, errors.New("gitlab: no projects configured")
	}

	var out []ConflictRef
	for _, project := range projects {
		refs, err := f.fetchProjectConflicts(ctx, project, since)
		if err != nil {
			return nil, errors.Wrapf(err, "project %s", project)
		}
		out = append(out, refs...)
	}
	return out, nil
}

func (f *Fetcher) fetchProjectConflicts(ctx context.Context, project string, since time.Time) ([]ConflictRef, error) {
	var out []ConflictRef

	for page := 1; page <= conflictMaxPages; page++ {
		q := url.Values{}
		q.Set("page", strconv.Itoa(page))
		q.Set("per_page", strconv.Itoa(f.pageSize))
		q.Set("state", "opened")
		q.Set("order_by", "updated_at")
		q.Set("sort", "desc")
		// GitLab recomputes mergeability only on request, and only
		// asynchronously: a "checking" MR is reported as not conflicting on
		// this pass and settles by the next tick.
		q.Set("with_merge_status_recheck", "true")
		if !since.IsZero() {
			q.Set("updated_after", since.UTC().Format(time.RFC3339))
		}

		req, err := f.buildRequest(ctx, fmt.Sprintf("/api/v4/projects/%s/merge_requests", encodeProjectRef(project)), q)
		if err != nil {
			return nil, err
		}
		body, err := f.doRequest(req, "fetcher.FetchConflicts")
		if err != nil {
			return nil, err
		}

		var mrs []gitlabMergeRequest
		if err := json.Unmarshal(body, &mrs); err != nil {
			return nil, errors.Wrap(err, "parse merge requests response")
		}

		for _, mr := range mrs {
			if !conflicting(mr) {
				continue
			}
			updated, err := parseGitLabTime(mr.UpdatedAt)
			if err != nil {
				return nil, errors.Wrapf(err, "mr !%d updated_at", mr.IID)
			}
			out = append(out, ConflictRef{Project: project, MR: ConflictMR{
				IID:          mr.IID,
				Title:        mr.Title,
				WebURL:       mr.WebURL,
				SourceBranch: mr.SourceBranch,
				TargetBranch: mr.TargetBranch,
				SHA:          mr.SHA,
				Author:       convertGitLabUser(mr.Author),
				Assignees:    usernames(mr.Assignees),
				Updated:      updated,
				Status:       mr.DetailedMergeStatus,
			}})
		}

		if len(mrs) < f.pageSize {
			break
		}
	}
	return out, nil
}

// conflicting reports whether GitLab says the MR cannot merge because of a
// conflict. Both fields are read because has_conflicts is the older, always
// present one while detailed_merge_status is the one GitLab documents as
// authoritative, and an instance may populate either.
func conflicting(mr gitlabMergeRequest) bool {
	return mr.HasConflicts || mr.DetailedMergeStatus == detailedStatusConflict
}

// usernames maps API users onto their match keys, falling back to the display
// name when the instance omitted the username, as the MR conversion does.
func usernames(users []*gitlabUser) []string {
	var out []string
	for _, u := range users {
		if u == nil {
			continue
		}
		name := u.Username
		if name == "" {
			name = u.Name
		}
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}
