// Package jira fetches Jira issues via the REST API, returning the
// chunker's structured chunkjira.Issue type. It knows nothing about
// index.Document or notifications: converting a fetched issue into an
// index.Document is internal/chunk/jira's job (DocumentFromIssue), called by
// whichever consumer needs it (internal/ingestrun for indexing,
// internal/notify/jira for notifications) — so this package has exactly one
// responsibility: talk to the Jira REST API. It supports Jira Cloud (Basic
// auth with email + API token) and Jira Server/DC (Basic auth with username
// + password, or PAT as Bearer token).
package jira

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	// JQL bounds are rendered in the Jira user's timezone (see buildJQL), which
	// means time.LoadLocation must work wherever this runs. The runtime image
	// ships no /usr/share/zoneinfo, and LoadLocation's failure mode here is a
	// silent fall back to UTC — i.e. a wrong query bound that skips issues — so
	// embed the database rather than depend on the base image carrying it.
	_ "time/tzdata"
	"unicode/utf8"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"go.uber.org/zap"

	chunkjira "github.com/go-faster/sisyphus/internal/chunk/jira"
	"github.com/go-faster/sisyphus/internal/cliversion"
	"github.com/go-faster/sisyphus/internal/netclient"
)

// Options configures a Jira Fetcher.
type Options struct {
	BaseURL    string
	Email      string // Cloud: user email for Basic auth
	Username   string // Server/DC: username for Basic auth
	APIToken   string // Cloud: API token for Basic auth
	Password   string // Server/DC: password for Basic auth
	PAT        string // Server/DC: personal access token
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
			opts.UserAgent = info.UserAgent("jira")
		}
	}
}

// FetchOptions controls a single incremental fetch.
type FetchOptions struct {
	Projects     []string  // e.g. ["BILL"]; required
	Fields       []string  // optional; defaults to ["*all"]
	UpdatedAfter time.Time // optional; if zero, no updated >= clause
	StartAt      int       // page offset; default 0
	PageSize     int       // override Options.PageSize if set
}

// Cursor is the persisted per-source cursor, JSON-encoded into
// SyncState.last_cursor. It is the full state needed to resume an incremental
// backfill across Jira's mid-backfill update race.
type Cursor struct {
	LastUpdated string `json:"last_updated"` // RFC3339 timestamp
	StartAt     int    `json:"start_at"`     // page offset within that bound
}

// AuthStatus describes the Jira identity used by the configured credentials.
type AuthStatus struct {
	AccountID    string
	Name         string
	Key          string
	DisplayName  string
	EmailAddress string
}

// Fetcher retrieves Jira issues via the REST API.
type Fetcher struct {
	baseURL    string
	email      string
	username   string
	apiToken   string
	password   string
	pat        string
	httpClient *http.Client
	pageSize   int
	userAgent  string

	// tz is the authenticated user's timezone, used to render JQL date bounds.
	// Resolved at most once via tzOnce, from CheckAuth's /myself response when
	// available and lazily by location otherwise; never nil after tzOnce runs.
	tzOnce sync.Once
	tz     *time.Location
}

// New creates a new Fetcher. Returns an error if no credentials are configured.
func New(opts Options) (*Fetcher, error) {
	if opts.PAT == "" && (opts.Username == "" || opts.Password == "") && (opts.Email == "" || opts.APIToken == "") {
		return nil, errors.New("jira: no credentials configured")
	}
	opts.setDefaults()
	return &Fetcher{
		baseURL:    opts.BaseURL,
		email:      opts.Email,
		username:   opts.Username,
		apiToken:   opts.APIToken,
		password:   opts.Password,
		pat:        opts.PAT,
		httpClient: opts.HTTPClient,
		pageSize:   opts.PageSize,
		userAgent:  opts.UserAgent,
	}, nil
}

type jiraSearchResponse struct {
	StartAt    int         `json:"startAt"`
	MaxResults int         `json:"maxResults"`
	Total      int         `json:"total"`
	Issues     []jiraIssue `json:"issues"`
}

type jiraUserResponse struct {
	AccountID    string `json:"accountId"`
	Name         string `json:"name"`
	Key          string `json:"key"`
	DisplayName  string `json:"displayName"`
	EmailAddress string `json:"emailAddress"`
	TimeZone     string `json:"timeZone"`
}

type jiraIssue struct {
	ID     string     `json:"id"`
	Key    string     `json:"key"`
	Fields jiraFields `json:"fields"`
	// Changelog is present only because the search asks for it (expand=changelog);
	// it names who actually performed an update, which no field does.
	Changelog *jiraChangelog `json:"changelog"`
}

type jiraFields struct {
	Summary        string                `json:"summary"`
	Description    any                   `json:"description"`
	Status         *jiraNamed            `json:"status"`
	Resolution     *jiraNamed            `json:"resolution"`
	Created        string                `json:"created"`
	Updated        string                `json:"updated"`
	ResolutionDate *string               `json:"resolutiondate"`
	Components     []jiraNamed           `json:"components"`
	Labels         []string              `json:"labels"`
	Assignee       *jiraUser             `json:"assignee"`
	Reporter       *jiraUser             `json:"reporter"`
	Comment        *jiraCommentContainer `json:"comment"`
}

type jiraNamed struct {
	Name string `json:"name"`
}

type jiraUser struct {
	AccountID   string `json:"accountId"`
	DisplayName string `json:"displayName"`
	// Name and Key are what Jira Server/Data Center returns; only Cloud has
	// accountId. Without them a Server deployment reports an empty identity
	// for every assignee, and no notification can ever match a user.
	Name string `json:"name"`
	Key  string `json:"key"`
}

// identity is the assignee's stable id: accountId on Cloud, name (or key) on
// Server/DC. It is what notify.identities is matched against.
func (u jiraUser) identity() string {
	for _, s := range []string{u.AccountID, u.Name, u.Key} {
		if s != "" {
			return s
		}
	}
	return ""
}

// profileURL is the user's browsable profile page under baseURL, or "" when
// the user object names nobody addressable.
//
// The two deployments do not address a user the same way, and an identity()
// string alone cannot say which is which — so the branch lives here, next to
// the raw API object, rather than anywhere downstream: only Cloud returns an
// accountId, so its presence is what distinguishes the two.
func (u jiraUser) profileURL(baseURL string) string {
	if baseURL == "" {
		return ""
	}
	if u.AccountID != "" {
		return baseURL + "/jira/people/" + url.PathEscape(u.AccountID)
	}
	name := u.Name
	if name == "" {
		name = u.Key
	}
	if name == "" {
		return ""
	}
	return baseURL + "/secure/ViewProfile.jspa?name=" + url.QueryEscape(name)
}

type jiraCommentContainer struct {
	Comments []jiraCommentItem `json:"comments"`
}

type jiraCommentItem struct {
	ID      string    `json:"id"`
	Author  *jiraUser `json:"author"`
	Body    string    `json:"body"`
	Created string    `json:"created"`
	Updated string    `json:"updated"`
}

var jiraTimeLayouts = []string{
	"2006-01-02T15:04:05.000-0700",
	"2006-01-02T15:04:05.000Z",
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02T15:04:05.000Z07:00",
}

func parseJiraTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	for _, layout := range jiraTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, errors.New("cannot parse jira time: " + s)
}

func descriptionString(d any) string {
	if d == nil {
		return ""
	}
	switch v := d.(type) {
	case string:
		return v
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// convertIssue maps a fetched issue onto the chunker's type. baseURL is only
// needed to build user profile links, and may be empty.
func convertIssue(ctx context.Context, jiraIss jiraIssue, baseURL string) (chunkjira.Issue, error) {
	iss := chunkjira.Issue{
		Key:   jiraIss.Key,
		Title: jiraIss.Fields.Summary,
	}

	iss.Description = descriptionString(jiraIss.Fields.Description)

	if jiraIss.Fields.Status != nil {
		iss.Status = jiraIss.Fields.Status.Name
	}
	if jiraIss.Fields.Resolution != nil {
		iss.Resolution = jiraIss.Fields.Resolution.Name
	}

	created, err := parseJiraTime(jiraIss.Fields.Created)
	if err != nil {
		return chunkjira.Issue{}, err
	}
	iss.Created = created

	updated, err := parseJiraTime(jiraIss.Fields.Updated)
	if err != nil {
		return chunkjira.Issue{}, err
	}
	iss.Updated = updated

	if jiraIss.Fields.ResolutionDate != nil {
		resolved, err := parseJiraTime(*jiraIss.Fields.ResolutionDate)
		if err != nil {
			return chunkjira.Issue{}, err
		}
		iss.Resolved = resolved
	}

	for _, c := range jiraIss.Fields.Components {
		iss.Components = append(iss.Components, c.Name)
	}

	iss.Labels = jiraIss.Fields.Labels

	if jiraIss.Fields.Assignee != nil {
		iss.Assignee = jiraIss.Fields.Assignee.DisplayName
		iss.AssigneeAccountID = jiraIss.Fields.Assignee.identity()
	}
	if jiraIss.Fields.Reporter != nil {
		iss.Reporter = jiraIss.Fields.Reporter.DisplayName
		iss.ReporterURL = jiraIss.Fields.Reporter.profileURL(baseURL)
	}

	actors := changelogActors(ctx, jiraIss.Changelog, baseURL, jiraIss.Key)
	iss.UpdatedBy, iss.AssignedBy, iss.AssignedAt = actors.UpdatedBy, actors.AssignedBy, actors.AssignedAt

	// An assignee the changelog never records anyone setting has been there
	// since the issue was filed, so the issue's own creation time dates the
	// assignment. Without this the assignment reads as "when unknown", which
	// staleness passes at any age. Only the assigner stays unknown: nothing
	// says who filled the field in, and the reporter is who filed the issue,
	// not necessarily who assigned it.
	if iss.AssigneeAccountID != "" && iss.AssignedAt.IsZero() && assignedAtCreation(jiraIss.Changelog) {
		iss.AssignedAt = iss.Created
	}

	if jiraIss.Fields.Comment != nil {
		for _, c := range jiraIss.Fields.Comment.Comments {
			cmt := chunkjira.Comment{
				ID:   c.ID,
				Body: c.Body,
			}
			if c.Author != nil {
				cmt.Author = c.Author.DisplayName
				cmt.AuthorUser = chunkjira.User{
					ID:      c.Author.identity(),
					Display: c.Author.DisplayName,
					URL:     c.Author.profileURL(baseURL),
				}
			}
			created, err := parseJiraTime(c.Created)
			if err != nil {
				return chunkjira.Issue{}, err
			}
			cmt.Created = created
			iss.Comments = append(iss.Comments, cmt)
		}
	}

	return iss, nil
}

// jqlDateFormat is the only layout JQL date literals accept for the
// "updated" field: "yyyy-MM-dd HH:mm" (no seconds, no timezone offset).
const jqlDateFormat = "2006-01-02 15:04"

// buildJQL renders the incremental query. loc must be the authenticated user's
// timezone: a JQL date literal carries no offset and Jira evaluates it in that
// zone, so the cursor (held in UTC) has to be converted to wall-clock time
// there. Rendering the UTC clock instead shifts the bound by the zone's offset
// — harmlessly widening the window east of UTC, but moving it *forward* west of
// UTC, which silently skips every issue updated inside the gap.
//
// Truncation to whole minutes only ever rounds the bound down, so the window
// stays inclusive of the cursor.
func buildJQL(projects []string, cursor Cursor, opts FetchOptions, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	quoted := make([]string, len(projects))
	for i, p := range projects {
		quoted[i] = `"` + p + `"`
	}
	jql := "project IN (" + strings.Join(quoted, ", ") + ")"

	var updatedAfter time.Time
	if cursor.LastUpdated != "" {
		if t, err := time.Parse(time.RFC3339, cursor.LastUpdated); err == nil {
			updatedAfter = t
		}
	} else if !opts.UpdatedAfter.IsZero() {
		updatedAfter = opts.UpdatedAfter
	}
	if !updatedAfter.IsZero() {
		jql += ` AND updated >= "` + updatedAfter.In(loc).Format(jqlDateFormat) + `"`
	}

	jql += " ORDER BY updated ASC"
	return jql
}

func (f *Fetcher) buildAPIRequest(ctx context.Context, path string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.baseURL+path, http.NoBody)
	if err != nil {
		return nil, errors.Wrap(err, "create request")
	}
	req.Header.Set("Accept", "application/json")
	if f.userAgent != "" {
		req.Header.Set("User-Agent", f.userAgent)
	}

	switch {
	case f.pat != "":
		req.Header.Set("Authorization", "Bearer "+f.pat)
	case f.username != "" && f.password != "":
		auth := base64.StdEncoding.EncodeToString([]byte(f.username + ":" + f.password))
		req.Header.Set("Authorization", "Basic "+auth)
	case f.email != "" && f.apiToken != "":
		auth := base64.StdEncoding.EncodeToString([]byte(f.email + ":" + f.apiToken))
		req.Header.Set("Authorization", "Basic "+auth)
	default:
		return nil, errors.New("jira: no credentials configured")
	}
	return req, nil
}

func (f *Fetcher) buildRequest(ctx context.Context, jql string, startAt, pageSize int, fields string) (*http.Request, error) {
	u, err := url.Parse(f.baseURL + "/rest/api/2/search")
	if err != nil {
		return nil, errors.Wrap(err, "parse url")
	}
	q := url.Values{}
	q.Set("jql", jql)
	q.Set("startAt", strconv.Itoa(startAt))
	q.Set("maxResults", strconv.Itoa(pageSize))
	q.Set("fields", fields)
	// No issue field says who performed an update — reporter is who filed it,
	// which is a different person as soon as anyone else touches the issue.
	// The changelog is the only place the actor exists, and asking for it here
	// costs one expand instead of a per-issue request.
	q.Set("expand", "changelog")
	u.RawQuery = q.Encode()

	req, err := f.buildAPIRequest(ctx, u.RequestURI())
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// CheckAuth verifies that Jira accepts the configured credentials and that the
// authenticated user can access every configured project.
func (f *Fetcher) CheckAuth(ctx context.Context, projects []string) (AuthStatus, error) {
	if len(projects) == 0 {
		return AuthStatus{}, errors.New("jira: empty projects list")
	}

	req, err := f.buildAPIRequest(ctx, "/rest/api/2/myself")
	if err != nil {
		return AuthStatus{}, err
	}
	body, err := f.doPreflight(req, "jira auth check")
	if err != nil {
		return AuthStatus{}, err
	}

	var user jiraUserResponse
	if err := json.Unmarshal(body, &user); err != nil {
		return AuthStatus{}, errors.Wrap(err, "parse jira auth check")
	}
	// This response also carries the timezone JQL bounds are rendered in; seed
	// it here so Fetch does not request /myself a second time.
	f.cacheLocation(ctx, user.TimeZone)
	status := AuthStatus{
		AccountID:    user.AccountID,
		Name:         user.Name,
		Key:          user.Key,
		DisplayName:  user.DisplayName,
		EmailAddress: user.EmailAddress,
	}

	for _, project := range projects {
		project = strings.TrimSpace(project)
		if project == "" {
			continue
		}
		req, err := f.buildAPIRequest(ctx, "/rest/api/2/project/"+url.PathEscape(project))
		if err != nil {
			return AuthStatus{}, err
		}
		if _, err := f.doPreflight(req, "jira project "+project+" check"); err != nil {
			return AuthStatus{}, err
		}
	}

	return status, nil
}

func formatErrorBody(resp *http.Response, body []byte) string {
	if resp.StatusCode == http.StatusUnauthorized {
		return "unauthorized"
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		return "html response hidden"
	}
	s := string(body)
	if utf8.RuneCountInString(s) > 256 {
		runes := []rune(s)
		return string(runes[:256]) + "..."
	}
	return s
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
		return nil, errors.Errorf("%s status %d: %s", op, resp.StatusCode, formatErrorBody(resp, body))
	}

	return body, nil
}

func (f *Fetcher) doPreflight(req *http.Request, op string) ([]byte, error) {
	return f.doRequest(req, op)
}

// FetchIssues performs ONE page of Jira's /rest/api/2/search endpoint,
// returning the issues and an advanced cursor.
func (f *Fetcher) FetchIssues(ctx context.Context, opts FetchOptions, cursor Cursor) ([]chunkjira.Issue, Cursor, bool, error) {
	if len(opts.Projects) == 0 {
		return nil, Cursor{}, false, errors.New("jira: empty projects list")
	}

	pageSize := f.pageSize
	if opts.PageSize > 0 {
		pageSize = opts.PageSize
	}

	fields := "*all"
	if len(opts.Fields) > 0 {
		fields = strings.Join(opts.Fields, ",")
	}

	loc := f.location(ctx)
	jql := buildJQL(opts.Projects, cursor, opts, loc)
	zctx.From(ctx).Debug("jira fetch",
		zap.String("jql", jql),
		zap.String("timezone", loc.String()),
		zap.Int("start_at", cursor.StartAt),
		zap.Int("page_size", pageSize),
		zap.String("fields", fields),
	)
	req, err := f.buildRequest(ctx, jql, cursor.StartAt, pageSize, fields)
	if err != nil {
		return nil, Cursor{}, false, errors.Wrap(err, "build request")
	}

	body, err := f.doRequest(req, "jira search")
	if err != nil {
		return nil, Cursor{}, false, err
	}

	var searchResp jiraSearchResponse
	if err := json.Unmarshal(body, &searchResp); err != nil {
		return nil, Cursor{}, false, errors.Wrap(err, "parse response")
	}

	issues := make([]chunkjira.Issue, 0, len(searchResp.Issues))
	for _, iss := range searchResp.Issues {
		chunkIssue, err := convertIssue(ctx, iss, f.baseURL)
		if err != nil {
			zctx.From(ctx).Warn("skipping issue with unparseable time",
				zap.String("key", iss.Key),
				zap.Error(err),
			)
			continue
		}
		if f.baseURL != "" && chunkIssue.Key != "" {
			chunkIssue.WebURL = f.baseURL + "/browse/" + chunkIssue.Key
		}
		issues = append(issues, chunkIssue)
	}

	if len(issues) == 0 {
		return issues, cursor, false, nil
	}

	lastUpdatedStr := issues[len(issues)-1].Updated.Format(time.RFC3339)

	var nextCursor Cursor
	var hasMore bool

	if len(issues) < pageSize {
		// Partial page: the current window may be exhausted.
		//
		// Mid-page ambiguous update race: if we are advancing by offset
		// (StartAt > 0) and the last issue's updated still equals the cursor
		// boundary, we cannot advance LastUpdated (that would re-fetch the
		// same issues).  Continue paging by offset until we see an issue
		// with updated strictly greater than LastUpdated.
		if cursor.StartAt > 0 && lastUpdatedStr == cursor.LastUpdated {
			nextCursor = Cursor{
				LastUpdated: cursor.LastUpdated,
				StartAt:     cursor.StartAt + len(issues),
			}
			hasMore = true
		} else {
			nextCursor = Cursor{
				LastUpdated: lastUpdatedStr,
				StartAt:     0,
			}
			hasMore = false
		}
	} else {
		// Full page: advance offset within the same time window.
		nextCursor = Cursor{
			LastUpdated: cursor.LastUpdated,
			StartAt:     cursor.StartAt + pageSize,
		}
		hasMore = true
	}

	return issues, nextCursor, hasMore, nil
}
