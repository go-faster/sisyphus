package ingestrun

import (
	"context"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"

	gitlabingest "github.com/go-faster/sisyphus/internal/ingest/gitlab"
	jiraingest "github.com/go-faster/sisyphus/internal/ingest/jira"
	"github.com/go-faster/sisyphus/internal/netclient"
)

// NewGitLabFetcher builds the GitLab fetcher and the project refs it covers,
// returning [ErrNotConfigured] when GitLab is not set up.
//
// It exists so ingestion and reconciliation reach GitLab through identical
// wiring — same proxy, same disk cache, same user agent, same auth preflight.
// A reconcile that talked to GitLab any differently could see a different set
// of objects than the run that indexed them, and the difference between those
// two sets is what it deletes.
func (r Runner) NewGitLabFetcher(ctx context.Context) (*gitlabingest.Fetcher, []string, error) {
	cfg := r.Config

	projects := GitLabProjectRefs(cfg.GitLab.Projects)
	if cfg.GitLab.BaseURL == "" || cfg.GitLab.Token == "" || len(projects) == 0 {
		zctx.From(ctx).Info("gitlab not configured")
		return nil, nil, ErrNotConfigured
	}

	cache, err := AuthenticatedHTTPCache("gitlab", cfg.GitLab.BaseURL, cfg.GitLab.Token)
	if err != nil {
		return nil, nil, errors.Wrap(err, "gitlab http cache")
	}

	httpClient, err := netclient.HTTPClient(ctx, "gitlab", cfg.Proxies.GitLab, netclient.HTTPClientOptions{
		TracerProvider: r.TP,
		MeterProvider:  r.MP,
		Cache:          cache,
		UserAgent:      r.UserAgent,
	})
	if err != nil {
		return nil, nil, errors.Wrap(err, "gitlab http client")
	}

	fetcher, err := gitlabingest.New(gitlabingest.Options{
		BaseURL:    cfg.GitLab.BaseURL,
		Token:      cfg.GitLab.Token,
		Projects:   projects,
		HTTPClient: httpClient,
		UserAgent:  r.UserAgent,
	})
	if err != nil {
		return nil, nil, errors.Wrap(err, "gitlab new fetcher")
	}
	if err := fetcher.CheckAuth(ctx); err != nil {
		return nil, nil, errors.Wrap(err, "gitlab auth check")
	}
	return fetcher, projects, nil
}

// NewJiraFetcher builds the Jira fetcher, the project keys it covers, and the
// preflight's auth status, returning [ErrNotConfigured] when Jira is not set
// up. See [Runner.NewGitLabFetcher] for why this is shared.
func (r Runner) NewJiraFetcher(ctx context.Context) (*jiraingest.Fetcher, []string, jiraingest.AuthStatus, error) {
	jc := r.Config.Jira

	// Gated on credentials, not on projects: an install with a token but no
	// projects is misconfigured, while ingestion without credentials is simply
	// not set up.
	if jc.BaseURL == "" || (jc.PAT == "" && (jc.Username == "" || jc.Password == "") && (jc.Email == "" || jc.APIToken == "")) {
		zctx.From(ctx).Info("jira not configured")
		return nil, nil, jiraingest.AuthStatus{}, ErrNotConfigured
	}

	cache, err := AuthenticatedHTTPCache("jira", jc.BaseURL, jc.Email, jc.Username, jc.APIToken, jc.Password, jc.PAT)
	if err != nil {
		return nil, nil, jiraingest.AuthStatus{}, errors.Wrap(err, "jira http cache")
	}

	httpClient, err := netclient.HTTPClient(ctx, "jira", r.Config.Proxies.Jira, netclient.HTTPClientOptions{
		TracerProvider: r.TP,
		MeterProvider:  r.MP,
		Cache:          cache,
		UserAgent:      r.UserAgent,
	})
	if err != nil {
		return nil, nil, jiraingest.AuthStatus{}, errors.Wrap(err, "jira http client")
	}

	fetcher, err := jiraingest.New(jiraingest.Options{
		BaseURL:    jc.BaseURL,
		Email:      jc.Email,
		Username:   jc.Username,
		APIToken:   jc.APIToken,
		Password:   jc.Password,
		PAT:        jc.PAT,
		HTTPClient: httpClient,
		UserAgent:  r.UserAgent,
	})
	if err != nil {
		return nil, nil, jiraingest.AuthStatus{}, errors.Wrap(err, "jira new fetcher")
	}

	projects := JiraProjectKeys(jc.Projects)
	authStatus, err := fetcher.CheckAuth(ctx, projects)
	if err != nil {
		return nil, nil, jiraingest.AuthStatus{}, errors.Wrap(err, "jira preflight")
	}
	return fetcher, projects, authStatus, nil
}
