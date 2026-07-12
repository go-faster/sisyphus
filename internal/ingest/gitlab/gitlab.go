// Package gitlab ingests GitLab issues, merge requests, and releases via the REST API
// into normalized Documents (plan §5).
package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"go.uber.org/zap"

	chunkgitlab "github.com/go-faster/sisyphus/internal/chunk/gitlab"
	"github.com/go-faster/sisyphus/internal/cliversion"
	"github.com/go-faster/sisyphus/internal/index"
	"github.com/go-faster/sisyphus/internal/netclient"
)

// Options configures a GitLab Fetcher.
type Options struct {
	BaseURL    string
	Token      string
	Projects   []string // Project IDs or paths.
	HTTPClient *http.Client
	PageSize   int // default 100 via setDefaults
	UserAgent  string
}

func (opts *Options) setDefaults() {
	if opts.HTTPClient == nil {
		opts.HTTPClient = http.DefaultClient
	}
	if opts.PageSize <= 0 {
		opts.PageSize = 100
	}
	opts.BaseURL = strings.TrimRight(opts.BaseURL, "/")
	if opts.UserAgent == "" {
		if info, ok := cliversion.GetInfo("github.com/go-faster/sisyphus"); ok {
			opts.UserAgent = info.UserAgent("gitlab")
		}
	}
}

// Cursor persists the incremental fetch state, JSON-encoded into SyncState.last_cursor.
// One cursor per resource type (issues, MRs, releases).
type Cursor struct {
	UpdatedAfter string `json:"updated_after"` // RFC3339 timestamp
}

// FetchResult is returned by a single Fetch call.
type FetchResult struct {
	Documents  []index.Document
	NextCursor Cursor
	HasMore    bool
}

// Fetcher retrieves GitLab issues, MRs, and releases via the REST API.
type Fetcher struct {
	baseURL    string
	token      string
	projects   []string
	httpClient *http.Client
	pageSize   int
	userAgent  string
}

// New creates a new Fetcher. Returns an error if no token is configured.
func New(opts Options) (*Fetcher, error) {
	if opts.Token == "" {
		return nil, errors.New("gitlab: token is required")
	}
	if opts.BaseURL == "" {
		return nil, errors.New("gitlab: base_url is required")
	}
	opts.setDefaults()
	return &Fetcher{
		baseURL:    opts.BaseURL,
		token:      opts.Token,
		projects:   append([]string(nil), opts.Projects...),
		httpClient: opts.HTTPClient,
		pageSize:   opts.PageSize,
		userAgent:  opts.UserAgent,
	}, nil
}

// GitLab API response types.

type gitlabIssue struct {
	IID         int           `json:"iid"`
	ProjectID   int           `json:"project_id"`
	Title       string        `json:"title"`
	State       string        `json:"state"`
	CreatedAt   string        `json:"created_at"`
	UpdatedAt   string        `json:"updated_at"`
	WebURL      string        `json:"web_url"`
	Labels      []string      `json:"labels"`
	Author      *gitlabUser   `json:"author"`
	Description string        `json:"description"`
	Assignees   []*gitlabUser `json:"assignees"`
}

type gitlabMergeRequest struct {
	IID            int           `json:"iid"`
	ProjectID      int           `json:"project_id"`
	Title          string        `json:"title"`
	State          string        `json:"state"`
	CreatedAt      string        `json:"created_at"`
	UpdatedAt      string        `json:"updated_at"`
	WebURL         string        `json:"web_url"`
	Labels         []string      `json:"labels"`
	Author         *gitlabUser   `json:"author"`
	Description    string        `json:"description"`
	Assignees      []*gitlabUser `json:"assignees"`
	Reviewers      []*gitlabUser `json:"reviewers"`
	Draft          bool          `json:"draft"`
	TargetBranch   string        `json:"target_branch"`
	SourceBranch   string        `json:"source_branch"`
	MergedAt       string        `json:"merged_at"`
	MergedBy       *gitlabUser   `json:"merged_by"`
	MergeCommitSHA string        `json:"merge_commit_sha"`
}

type gitlabRelease struct {
	TagName     string       `json:"tag_name"`
	Name        string       `json:"name"`
	CreatedAt   string       `json:"created_at"`
	ReleasedAt  string       `json:"released_at"`
	Description string       `json:"description"`
	Links       *gitlabLinks `json:"_links"`
}

type gitlabLinks struct {
	Self string `json:"self"`
}

type gitlabUser struct {
	Username string `json:"username"`
	Name     string `json:"name"`
}

type gitlabNote struct {
	ID         int         `json:"id"`
	System     bool        `json:"system"`
	Body       string      `json:"body"`
	CreatedAt  string      `json:"created_at"`
	Author     *gitlabUser `json:"author"`
	Resolvable bool        `json:"resolvable"`
	Resolved   bool        `json:"resolved"`
}

type gitlabDiscussion struct {
	ID             string       `json:"id"`
	IndividualNote bool         `json:"individual_note"`
	Notes          []gitlabNote `json:"notes"`
}

type gitlabLinkedItem struct {
	IID      int    `json:"iid"`
	Title    string `json:"title"`
	WebURL   string `json:"web_url"`
	LinkType string `json:"link_type"`
}

func parseGitLabTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	// GitLab uses RFC3339 format
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, errors.Wrap(err, "parse gitlab time")
	}
	return t.UTC(), nil
}

func (f *Fetcher) buildRequest(ctx context.Context, path string, query url.Values) (*http.Request, error) {
	u, err := url.Parse(f.baseURL + path)
	if err != nil {
		return nil, errors.Wrap(err, "parse url")
	}
	u.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return nil, errors.Wrap(err, "create request")
	}

	req.Header.Set("PRIVATE-TOKEN", f.token)
	req.Header.Set("Accept", "application/json")
	if f.userAgent != "" {
		req.Header.Set("User-Agent", f.userAgent)
	}
	return req, nil
}

func (f *Fetcher) doRequest(req *http.Request, op string) ([]byte, error) {
	resp, err := netclient.DoWithRetry(req.Context(), op, f.httpClient, req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.Wrap(err, "read "+op+" response")
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errors.Errorf("%s status %d: %s", op, resp.StatusCode, string(body))
	}

	return body, nil
}

// CheckAuth verifies that GitLab accepts the configured token.
func (f *Fetcher) CheckAuth(ctx context.Context) error {
	q := url.Values{}
	req, err := f.buildRequest(ctx, "/api/v4/version", q)
	if err != nil {
		return err
	}

	_, err = f.doRequest(req, "fetcher.CheckAuth")
	return err
}

func projectRefs(projects []string) []string {
	if len(projects) == 0 {
		return nil
	}
	out := make([]string, 0, len(projects))
	for _, p := range projects {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// encodeProjectRef URL-encodes a project ID or path for use in URLs.
func encodeProjectRef(ref string) string {
	return url.PathEscape(ref)
}

// FetchIssues fetches issues from configured projects, returning documents and the updated cursor.
func (f *Fetcher) FetchIssues(
	ctx context.Context,
	page int,
	cursor Cursor,
) (FetchResult, error) {
	projects := projectRefs(f.projects)
	if len(projects) == 0 {
		return FetchResult{}, errors.New("gitlab: no projects configured")
	}

	var docs []index.Document
	var maxUpdatedAt string

	for _, project := range projects {
		projectDocs, projectMaxUpdated, err := f.fetchProjectIssues(ctx, project, page, cursor)
		if err != nil {
			return FetchResult{}, err
		}
		docs = append(docs, projectDocs...)
		if projectMaxUpdated > maxUpdatedAt {
			maxUpdatedAt = projectMaxUpdated
		}
	}

	result := FetchResult{
		Documents: docs,
		NextCursor: Cursor{
			UpdatedAfter: maxUpdatedAt,
		},
		HasMore: len(docs) >= f.pageSize,
	}

	return result, nil
}

func (f *Fetcher) fetchProjectIssues(ctx context.Context, project string, page int, cursor Cursor) ([]index.Document, string, error) {
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("per_page", strconv.Itoa(f.pageSize))
	q.Set("order_by", "updated_at")
	q.Set("sort", "asc")

	if cursor.UpdatedAfter != "" {
		q.Set("updated_after", cursor.UpdatedAfter)
	}

	path := fmt.Sprintf("/api/v4/projects/%s/issues", encodeProjectRef(project))
	req, err := f.buildRequest(ctx, path, q)
	if err != nil {
		return nil, "", err
	}

	body, err := f.doRequest(req, "fetcher.FetchProjectIssues")
	if err != nil {
		return nil, "", err
	}

	var issues []gitlabIssue
	if err := json.Unmarshal(body, &issues); err != nil {
		return nil, "", errors.Wrap(err, "parse issues response")
	}

	var docs []index.Document
	var maxUpdatedAt string

	for _, issue := range issues {
		// Fetch discussions (comments) and links for this issue
		threads, err := f.fetchIssueDiscussions(ctx, project, issue.IID)
		if err != nil {
			zctx.From(ctx).Warn("failed to fetch issue discussions",
				zap.String("project", project),
				zap.Int("iid", issue.IID),
				zap.Error(err),
			)
			threads = nil
		}

		links, err := f.fetchIssueLinks(ctx, project, issue.IID)
		if err != nil {
			zctx.From(ctx).Warn("failed to fetch issue links",
				zap.String("project", project),
				zap.Int("iid", issue.IID),
				zap.Error(err),
			)
			links = nil
		}

		chunkIssue, err := convertGitLabIssue(issue, threads, links)
		if err != nil {
			zctx.From(ctx).Warn("skipping issue with unparseable time",
				zap.String("project", project),
				zap.Int("iid", issue.IID),
				zap.Error(err),
			)
			continue
		}

		doc := chunkgitlab.DocumentFromIssue(project, chunkIssue)
		docs = append(docs, doc)

		if doc.UpdatedAt.Format(time.RFC3339) > maxUpdatedAt {
			maxUpdatedAt = doc.UpdatedAt.Format(time.RFC3339)
		}
	}

	return docs, maxUpdatedAt, nil
}

func (f *Fetcher) fetchIssueDiscussions(ctx context.Context, project string, iid int) ([]chunkgitlab.Thread, error) {
	q := url.Values{}
	q.Set("per_page", strconv.Itoa(100))
	q.Set("order_by", "created_at")
	q.Set("sort", "asc")

	path := fmt.Sprintf("/api/v4/projects/%s/issues/%d/discussions", encodeProjectRef(project), iid)
	req, err := f.buildRequest(ctx, path, q)
	if err != nil {
		return nil, err
	}

	body, err := f.doRequest(req, "fetcher.FetchIssueDiscussions")
	if err != nil {
		return nil, err
	}

	var discussions []gitlabDiscussion
	if err := json.Unmarshal(body, &discussions); err != nil {
		return nil, errors.Wrap(err, "parse discussions response")
	}

	var threads []chunkgitlab.Thread
	for _, discussion := range discussions {
		var comments []chunkgitlab.Comment
		resolved := false

		for _, note := range discussion.Notes {
			if note.System {
				continue // Skip system notes
			}

			created, err := parseGitLabTime(note.CreatedAt)
			if err != nil {
				continue // Skip notes with unparseable time
			}

			author := ""
			if note.Author != nil {
				author = note.Author.Username
				if author == "" {
					author = note.Author.Name
				}
			}

			comments = append(comments, chunkgitlab.Comment{
				Author:  author,
				Body:    note.Body,
				Created: created,
			})

			// Track if any note in the discussion is resolved
			if note.Resolved {
				resolved = true
			}
		}

		// Only include thread if it has substantive comments
		if len(comments) > 0 {
			threads = append(threads, chunkgitlab.Thread{
				ID:       discussion.ID,
				Resolved: resolved,
				Comments: comments,
			})
		}
	}

	return threads, nil
}

func (f *Fetcher) fetchIssueLinks(ctx context.Context, project string, iid int) ([]chunkgitlab.Link, error) {
	q := url.Values{}
	q.Set("per_page", strconv.Itoa(100))

	path := fmt.Sprintf("/api/v4/projects/%s/issues/%d/links", encodeProjectRef(project), iid)
	req, err := f.buildRequest(ctx, path, q)
	if err != nil {
		return nil, err
	}

	body, err := f.doRequest(req, "fetcher.FetchIssueLinks")
	if err != nil {
		// Non-fatal error: log and return nil
		zctx.From(ctx).Warn("failed to fetch issue links",
			zap.String("project", project),
			zap.Int("iid", iid),
			zap.Error(err),
		)
		return nil, nil
	}

	var items []gitlabLinkedItem
	if err := json.Unmarshal(body, &items); err != nil {
		zctx.From(ctx).Warn("failed to parse issue links response",
			zap.String("project", project),
			zap.Int("iid", iid),
			zap.Error(err),
		)
		return nil, nil
	}

	var links []chunkgitlab.Link
	for _, item := range items {
		links = append(links, chunkgitlab.Link{
			Type:       item.LinkType,
			TargetKind: "issue",
			TargetIID:  item.IID,
			Title:      item.Title,
			WebURL:     item.WebURL,
		})
	}

	return links, nil
}

// FetchMergeRequests fetches merge requests from configured projects.
func (f *Fetcher) FetchMergeRequests(
	ctx context.Context,
	page int,
	cursor Cursor,
) (FetchResult, error) {
	projects := projectRefs(f.projects)
	if len(projects) == 0 {
		return FetchResult{}, errors.New("gitlab: no projects configured")
	}

	var docs []index.Document
	var maxUpdatedAt string

	for _, project := range projects {
		projectDocs, projectMaxUpdated, err := f.fetchProjectMergeRequests(ctx, project, page, cursor)
		if err != nil {
			return FetchResult{}, err
		}
		docs = append(docs, projectDocs...)
		if projectMaxUpdated > maxUpdatedAt {
			maxUpdatedAt = projectMaxUpdated
		}
	}

	result := FetchResult{
		Documents: docs,
		NextCursor: Cursor{
			UpdatedAfter: maxUpdatedAt,
		},
		HasMore: len(docs) >= f.pageSize,
	}

	return result, nil
}

func (f *Fetcher) fetchProjectMergeRequests(ctx context.Context, project string, page int, cursor Cursor) ([]index.Document, string, error) {
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("per_page", strconv.Itoa(f.pageSize))
	q.Set("order_by", "updated_at")
	q.Set("sort", "asc")

	if cursor.UpdatedAfter != "" {
		q.Set("updated_after", cursor.UpdatedAfter)
	}

	path := fmt.Sprintf("/api/v4/projects/%s/merge_requests", encodeProjectRef(project))
	req, err := f.buildRequest(ctx, path, q)
	if err != nil {
		return nil, "", err
	}

	body, err := f.doRequest(req, "fetcher.FetchProjectMergeRequests")
	if err != nil {
		return nil, "", err
	}

	var mrs []gitlabMergeRequest
	if err := json.Unmarshal(body, &mrs); err != nil {
		return nil, "", errors.Wrap(err, "parse merge requests response")
	}

	var docs []index.Document
	var maxUpdatedAt string

	for _, mr := range mrs {
		// Fetch discussions (comments) and closes issues for this MR
		threads, err := f.fetchMRDiscussions(ctx, project, mr.IID)
		if err != nil {
			zctx.From(ctx).Warn("failed to fetch MR discussions",
				zap.String("project", project),
				zap.Int("iid", mr.IID),
				zap.Error(err),
			)
			threads = nil
		}

		links, err := f.fetchMRClosesIssues(ctx, project, mr.IID)
		if err != nil {
			zctx.From(ctx).Warn("failed to fetch MR closes issues",
				zap.String("project", project),
				zap.Int("iid", mr.IID),
				zap.Error(err),
			)
			links = nil
		}

		chunkMR, err := convertGitLabMR(mr, threads, links)
		if err != nil {
			zctx.From(ctx).Warn("skipping MR with unparseable time",
				zap.String("project", project),
				zap.Int("iid", mr.IID),
				zap.Error(err),
			)
			continue
		}

		doc := chunkgitlab.DocumentFromMergeRequest(project, chunkMR)
		docs = append(docs, doc)

		if doc.UpdatedAt.Format(time.RFC3339) > maxUpdatedAt {
			maxUpdatedAt = doc.UpdatedAt.Format(time.RFC3339)
		}
	}

	return docs, maxUpdatedAt, nil
}

func (f *Fetcher) fetchMRDiscussions(ctx context.Context, project string, iid int) ([]chunkgitlab.Thread, error) {
	q := url.Values{}
	q.Set("per_page", strconv.Itoa(100))
	q.Set("order_by", "created_at")
	q.Set("sort", "asc")

	path := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d/discussions", encodeProjectRef(project), iid)
	req, err := f.buildRequest(ctx, path, q)
	if err != nil {
		return nil, err
	}

	body, err := f.doRequest(req, "fetcher.FetchMRDiscussions")
	if err != nil {
		return nil, err
	}

	var discussions []gitlabDiscussion
	if err := json.Unmarshal(body, &discussions); err != nil {
		return nil, errors.Wrap(err, "parse discussions response")
	}

	var threads []chunkgitlab.Thread
	for _, discussion := range discussions {
		var comments []chunkgitlab.Comment
		resolved := false

		for _, note := range discussion.Notes {
			if note.System {
				continue // Skip system notes
			}

			created, err := parseGitLabTime(note.CreatedAt)
			if err != nil {
				continue // Skip notes with unparseable time
			}

			author := ""
			if note.Author != nil {
				author = note.Author.Username
				if author == "" {
					author = note.Author.Name
				}
			}

			comments = append(comments, chunkgitlab.Comment{
				Author:  author,
				Body:    note.Body,
				Created: created,
			})

			// Track if any note in the discussion is resolved
			if note.Resolved {
				resolved = true
			}
		}

		// Only include thread if it has substantive comments
		if len(comments) > 0 {
			threads = append(threads, chunkgitlab.Thread{
				ID:       discussion.ID,
				Resolved: resolved,
				Comments: comments,
			})
		}
	}

	return threads, nil
}

func (f *Fetcher) fetchMRClosesIssues(ctx context.Context, project string, iid int) ([]chunkgitlab.Link, error) {
	q := url.Values{}
	q.Set("per_page", strconv.Itoa(100))

	path := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d/closes_issues", encodeProjectRef(project), iid)
	req, err := f.buildRequest(ctx, path, q)
	if err != nil {
		return nil, err
	}

	body, err := f.doRequest(req, "fetcher.FetchMRClosesIssues")
	if err != nil {
		// Non-fatal error: log and return nil
		zctx.From(ctx).Warn("failed to fetch mr closes issues",
			zap.String("project", project),
			zap.Int("iid", iid),
			zap.Error(err),
		)
		return nil, nil
	}

	var items []gitlabLinkedItem
	if err := json.Unmarshal(body, &items); err != nil {
		zctx.From(ctx).Warn("failed to parse mr closes issues response",
			zap.String("project", project),
			zap.Int("iid", iid),
			zap.Error(err),
		)
		return nil, nil
	}

	var links []chunkgitlab.Link
	for _, item := range items {
		links = append(links, chunkgitlab.Link{
			Type:       "closes",
			TargetKind: "issue",
			TargetIID:  item.IID,
			Title:      item.Title,
			WebURL:     item.WebURL,
		})
	}

	return links, nil
}

// FetchReleases fetches releases from configured projects.
// Note: releases don't have updated_after filtering, so we filter client-side.
func (f *Fetcher) FetchReleases(
	ctx context.Context,
	page int,
	cursor Cursor,
) (FetchResult, error) {
	projects := projectRefs(f.projects)
	if len(projects) == 0 {
		return FetchResult{}, errors.New("gitlab: no projects configured")
	}

	var docs []index.Document
	var maxReleasedAt string

	for _, project := range projects {
		projectDocs, projectMaxReleased, err := f.fetchProjectReleases(ctx, project, page, cursor)
		if err != nil {
			return FetchResult{}, err
		}
		docs = append(docs, projectDocs...)
		if projectMaxReleased > maxReleasedAt {
			maxReleasedAt = projectMaxReleased
		}
	}

	result := FetchResult{
		Documents: docs,
		NextCursor: Cursor{
			UpdatedAfter: maxReleasedAt,
		},
		HasMore: len(docs) >= f.pageSize,
	}

	return result, nil
}

func (f *Fetcher) fetchProjectReleases(ctx context.Context, project string, page int, cursor Cursor) ([]index.Document, string, error) {
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("per_page", strconv.Itoa(f.pageSize))

	path := fmt.Sprintf("/api/v4/projects/%s/releases", encodeProjectRef(project))
	req, err := f.buildRequest(ctx, path, q)
	if err != nil {
		return nil, "", err
	}

	body, err := f.doRequest(req, "fetcher.FetchReleases")
	if err != nil {
		return nil, "", err
	}

	var releases []gitlabRelease
	if err := json.Unmarshal(body, &releases); err != nil {
		return nil, "", errors.Wrap(err, "parse releases response")
	}

	var docs []index.Document
	var maxReleasedAt string

	for _, release := range releases {
		// Filter by released_at if cursor is set
		if cursor.UpdatedAfter != "" {
			releasedAt, err := parseGitLabTime(release.ReleasedAt)
			if err != nil || releasedAt.Format(time.RFC3339) < cursor.UpdatedAfter {
				continue
			}
		}

		chunkRelease, err := convertGitLabRelease(release)
		if err != nil {
			zctx.From(ctx).Warn("skipping release with unparseable time",
				zap.String("project", project),
				zap.String("tag", release.TagName),
				zap.Error(err),
			)
			continue
		}

		doc := chunkgitlab.DocumentFromRelease(project, chunkRelease)
		docs = append(docs, doc)

		releasedAtStr := doc.UpdatedAt.Format(time.RFC3339)
		if releasedAtStr > maxReleasedAt {
			maxReleasedAt = releasedAtStr
		}
	}

	return docs, maxReleasedAt, nil
}

// Conversion functions from GitLab API types to chunker types.

func convertGitLabIssue(issue gitlabIssue, threads []chunkgitlab.Thread, links []chunkgitlab.Link) (chunkgitlab.Issue, error) {
	created, err := parseGitLabTime(issue.CreatedAt)
	if err != nil {
		return chunkgitlab.Issue{}, err
	}

	updated, err := parseGitLabTime(issue.UpdatedAt)
	if err != nil {
		return chunkgitlab.Issue{}, err
	}

	author := ""
	if issue.Author != nil {
		author = issue.Author.Username
		if author == "" {
			author = issue.Author.Name
		}
	}

	// Convert assignees to usernames
	var assignees []string
	for _, a := range issue.Assignees {
		if a != nil {
			username := a.Username
			if username == "" {
				username = a.Name
			}
			if username != "" {
				assignees = append(assignees, username)
			}
		}
	}

	return chunkgitlab.Issue{
		IID:         issue.IID,
		Title:       issue.Title,
		Description: issue.Description,
		State:       issue.State,
		Labels:      issue.Labels,
		Author:      author,
		WebURL:      issue.WebURL,
		Created:     created,
		Updated:     updated,
		Assignees:   assignees,
		Threads:     threads,
		Links:       links,
	}, nil
}

func convertGitLabMR(mr gitlabMergeRequest, threads []chunkgitlab.Thread, links []chunkgitlab.Link) (chunkgitlab.MergeRequest, error) {
	created, err := parseGitLabTime(mr.CreatedAt)
	if err != nil {
		return chunkgitlab.MergeRequest{}, err
	}

	updated, err := parseGitLabTime(mr.UpdatedAt)
	if err != nil {
		return chunkgitlab.MergeRequest{}, err
	}

	mergedAt, err := parseGitLabTime(mr.MergedAt)
	if err != nil {
		return chunkgitlab.MergeRequest{}, err
	}

	author := ""
	if mr.Author != nil {
		author = mr.Author.Username
		if author == "" {
			author = mr.Author.Name
		}
	}

	// Convert assignees to usernames
	var assignees []string
	for _, a := range mr.Assignees {
		if a != nil {
			username := a.Username
			if username == "" {
				username = a.Name
			}
			if username != "" {
				assignees = append(assignees, username)
			}
		}
	}

	// Convert reviewers to usernames
	var reviewers []string
	for _, r := range mr.Reviewers {
		if r != nil {
			username := r.Username
			if username == "" {
				username = r.Name
			}
			if username != "" {
				reviewers = append(reviewers, username)
			}
		}
	}

	// Convert merged_by to username
	mergedBy := ""
	if mr.MergedBy != nil {
		mergedBy = mr.MergedBy.Username
		if mergedBy == "" {
			mergedBy = mr.MergedBy.Name
		}
	}

	return chunkgitlab.MergeRequest{
		IID:            mr.IID,
		Title:          mr.Title,
		Description:    mr.Description,
		State:          mr.State,
		Labels:         mr.Labels,
		Author:         author,
		WebURL:         mr.WebURL,
		Created:        created,
		Updated:        updated,
		Assignees:      assignees,
		Reviewers:      reviewers,
		Draft:          mr.Draft,
		TargetBranch:   mr.TargetBranch,
		SourceBranch:   mr.SourceBranch,
		MergedAt:       mergedAt,
		MergedBy:       mergedBy,
		MergeCommitSHA: mr.MergeCommitSHA,
		Threads:        threads,
		Links:          links,
	}, nil
}

func convertGitLabRelease(release gitlabRelease) (chunkgitlab.Release, error) {
	releasedAt, err := parseGitLabTime(release.ReleasedAt)
	if err != nil {
		return chunkgitlab.Release{}, err
	}

	selfURL := ""
	if release.Links != nil {
		selfURL = release.Links.Self
	}

	return chunkgitlab.Release{
		TagName:     release.TagName,
		Name:        release.Name,
		Description: release.Description,
		ReleasedAt:  releasedAt,
		WebURL:      selfURL,
	}, nil
}
