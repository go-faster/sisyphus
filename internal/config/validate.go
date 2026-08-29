package config

import (
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/go-faster/errors"
	"github.com/go-faster/figureout"
)

// postResolve turns a resolved wire into the Config consumers read: it
// materializes secrets, applies the normalizations a descriptor cannot express
// (a constraint rejects a value, it does not rewrite one), and collects the
// warnings.
func postResolve(w wire, report *figureout.Report, baseDir string) (Config, error) {
	cfg := w.Config
	if err := w.Secrets.materialize(&cfg, baseDir); err != nil {
		return Config{}, err
	}

	warnings := deprecationWarnings(report)

	sites, siteWarnings, err := normalizeFetchSites(cfg.Fetch.Sites)
	if err != nil {
		return Config{}, err
	}
	cfg.Fetch.Sites = sites
	warnings = append(warnings, siteWarnings...)

	severity := strings.ToLower(strings.TrimSpace(cfg.Alertmanager.InvestigateMinSeverity))
	switch severity {
	case "", "info", "warning", "critical":
	default:
		return Config{}, errors.Errorf("alertmanager.investigate.min_severity %q must be info, warning or critical", severity)
	}
	cfg.Alertmanager.InvestigateMinSeverity = severity

	// notify.poll.interval_seconds no longer schedules any collection — the
	// GitLab and Jira ingestion runs emit the events now — but a config that
	// only set the notify cadence would stop notifying silently, so it seeds
	// whichever source poll is still unset.
	if n := cfg.Notify.PollIntervalSeconds; n > 0 {
		if cfg.GitLab.PollIntervalSeconds <= 0 || cfg.Jira.PollIntervalSeconds <= 0 {
			warnings = append(warnings,
				"notify.poll.interval_seconds now only sets ssbot's outbox drain interval; notification events come from the gitlab/jira ingestion runs, so set gitlab.poll.interval_seconds and jira.poll.interval_seconds")
		}
		if cfg.GitLab.PollIntervalSeconds <= 0 {
			cfg.GitLab.PollIntervalSeconds = n
		}
		if cfg.Jira.PollIntervalSeconds <= 0 {
			cfg.Jira.PollIntervalSeconds = n
		}
	}

	if cfg.GitLab.ConflictPollIntervalSeconds > 0 && !cfg.GitLab.MergeRequests {
		warnings = append(warnings,
			"gitlab.conflicts.interval_seconds is set but gitlab.merge_requests is off: the conflict sweep runs against projects whose MRs are not ingested")
	}

	// A default applies to an absent key, so a config that writes 0 meaning
	// "whatever the default is" still needs the floor these always had.
	clampPositive(&cfg.Ingest.Worker.Concurrency, defaultIngestWorkerConcurrency)
	clampPositive(&cfg.Ingest.Worker.LeaseSeconds, defaultIngestWorkerLeaseSeconds)
	clampPositive(&cfg.Ingest.Worker.MaxAttempts, defaultIngestWorkerMaxAttempts)
	clampPositive(&cfg.Ingest.Worker.PollIntervalSeconds, defaultIngestWorkerPollSeconds)

	cfg.Warnings = warnings
	return cfg, nil
}

func clampPositive(v *int, def int) {
	if *v <= 0 {
		*v = def
	}
}

// deprecationWarnings renders figureout's deprecation diagnostics in the
// phrasing LogWarnings has always used.
func deprecationWarnings(report *figureout.Report) []string {
	var out []string
	for _, d := range report.Diagnostics {
		if d.Severity != figureout.SeverityWarning || d.Code != figureout.CodeDeprecated {
			continue
		}
		if d.MovedTo != "" {
			out = append(out, d.FieldPath+" is deprecated, use "+d.MovedTo+" instead")
			continue
		}
		out = append(out, d.FieldPath+" is deprecated: "+d.Message)
	}
	return out
}

// normalizeFetchSites applies the per-site rewrites: methods uppercased and
// defaulted, credentials read out of the environment.
func normalizeFetchSites(in []FetchSite) (sites []FetchSite, warnings []string, err error) {
	sites = make([]FetchSite, 0, len(in))
	for _, site := range in {
		site.Name = strings.TrimSpace(site.Name)
		for _, pattern := range site.URLPatterns {
			if !strings.HasPrefix(pattern, "https://") && !strings.HasPrefix(pattern, "http://") {
				return nil, nil, errors.Errorf("fetch site %q pattern %q must start with http:// or https://", site.Name, pattern)
			}
		}

		methods, warns, err := normalizeFetchMethods(site.Methods)
		if err != nil {
			return nil, nil, errors.Wrap(err, "fetch site "+site.Name)
		}
		for _, warn := range warns {
			warnings = append(warnings,
				"fetch site "+site.Name+" allows write method "+warn+"; prefer read-only methods unless explicitly required")
		}
		site.Methods = methods

		creds, err := resolveFetchCredentials(site.Credentials)
		if err != nil {
			return nil, nil, errors.Wrap(err, "fetch site "+site.Name+" credentials")
		}
		site.Credentials = creds
		sites = append(sites, site)
	}
	return sites, warnings, nil
}

func normalizeFetchMethods(methods []string) (normalized, methodWarnings []string, err error) {
	if len(methods) == 0 {
		return []string{http.MethodGet}, nil, nil
	}
	valid := map[string]struct{}{
		http.MethodGet: {}, http.MethodHead: {}, http.MethodPost: {},
		http.MethodPut: {}, http.MethodPatch: {}, http.MethodDelete: {},
	}
	seen := make(map[string]struct{}, len(methods))
	out := make([]string, 0, len(methods))
	for _, method := range methods {
		method = strings.ToUpper(strings.TrimSpace(method))
		if method == "" {
			continue
		}
		if _, ok := valid[method]; !ok {
			return nil, nil, errors.Errorf("unsupported method %q", method)
		}
		if _, ok := seen[method]; ok {
			continue
		}
		seen[method] = struct{}{}
		out = append(out, method)
		switch method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			methodWarnings = append(methodWarnings, method)
		}
	}
	if len(out) == 0 {
		out = []string{http.MethodGet}
	}
	return out, methodWarnings, nil
}

func resolveFetchCredentials(creds FetchCredentials) (FetchCredentials, error) {
	creds.Type = strings.ToLower(strings.TrimSpace(creds.Type))
	if creds.Type == "" {
		creds.Type = "none"
	}
	switch creds.Type {
	case "none":
		return creds, nil
	case "bearer":
		if creds.TokenEnv == "" {
			return FetchCredentials{}, errors.New("token_env is required for bearer credentials")
		}
		creds.Token = os.Getenv(creds.TokenEnv)
		return creds, nil
	case "header":
		if creds.Header == "" || creds.TokenEnv == "" {
			return FetchCredentials{}, errors.New("header and token_env are required for header credentials")
		}
		creds.Token = os.Getenv(creds.TokenEnv)
		return creds, nil
	case "basic":
		if creds.Username == "" || creds.PasswordEnv == "" {
			return FetchCredentials{}, errors.New("username and password_env are required for basic credentials")
		}
		creds.Password = os.Getenv(creds.PasswordEnv)
		return creds, nil
	default:
		return FetchCredentials{}, errors.Errorf("unsupported type %q", creds.Type)
	}
}

// fetchProxySecret returns the configured proxy a site names, or nil when the
// name is unknown. It answers the same question internal/fetch's proxyURL does,
// before the secret is materialized — keep the two switches in sync.
func fetchProxySecret(s *secrets, name string) *Secret {
	var out *Secret
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "fetch":
		out = &s.ProxyFetch
	case "git":
		out = &s.ProxyGit
	case "gitlab":
		out = &s.ProxyGitLab
	case "jira":
		out = &s.ProxyJira
	case "ollama":
		out = &s.ProxyOllama
	case "openrouter":
		out = &s.ProxyOpenRouter
	default:
		return nil
	}
	if *out == (Secret{}) {
		return nil
	}
	return out
}

// sitePath and identityPath name an element for an invariant violation, so the
// report can point at the line that set it.
func sitePath(i int, field string) string {
	return "fetch.sites[" + strconv.Itoa(i) + "]." + field
}

func identityPath(i int) string {
	return "notify.identities[" + strconv.Itoa(i) + "]"
}
