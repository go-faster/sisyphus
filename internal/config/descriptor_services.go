package config

import (
	"time"

	"github.com/go-faster/figureout"
)

// describeAgent declares the /investigate service.
func describeAgent(c *wire, s *figureout.Schema[wire]) {
	figureout.Group(s, "agent", func(s *figureout.Schema[wire]) {
		figureout.Value(s, &c.Agent.Addr, "addr").ApplyDefault(":8082")
		figureout.Value(s, &c.Agent.BaseURL, "base_url").ApplyDefault("").
			Doc("where ssbot reaches ssagent")
		secret(s, &c.Secrets.AgentAuthToken, "auth_token")
		figureout.Value(s, &c.Agent.Model, "model").ApplyDefault("")
		figureout.Value(s, &c.Agent.MaxToolIterations, "max_tool_iterations").ApplyDefault(8)
		figureout.Value(s, &c.Agent.RequestTimeoutSeconds, "request_timeout_seconds").ApplyDefault(180)
		figureout.Value(s, &c.Agent.GatewayURL, "gateway_url").ApplyDefault("")
		figureout.Value(s, &c.Agent.MaxReportChars, "max_report_chars").ApplyDefault(1500)
		figureout.Value(s, &c.Agent.MaxConcurrent, "max_concurrent").ApplyDefault(4)
		figureout.Value(s, &c.Agent.MaxBodyBytes, "max_body_bytes").ApplyDefault(int64(64 * 1024))
		figureout.Value(s, &c.Agent.ShowDebugInfo, "show_debug_info").ApplyDefault(false)
	})
	figureout.Ignore(s, &c.Agent.AuthToken, figureout.Reason("materialized from agent.auth_token"))
}

// describeContext declares the /context answering workflow.
func describeContext(c *wire, s *figureout.Schema[wire]) {
	figureout.Group(s, "context", func(s *figureout.Schema[wire]) {
		figureout.Value(s, &c.Context.Agentic, "agentic").ApplyDefault(false).
			Doc("needs OpenRouter; falls back to the non-agentic answerer without it")
		figureout.Value(s, &c.Context.MaxIterations, "max_iterations").ApplyDefault(6)
		figureout.Value(s, &c.Context.TimeoutSeconds, "timeout_seconds").ApplyDefault(180).
			Doc("bounds the answerer; telegram.answer_timeout_seconds bounds the wait")
		figureout.Value(s, &c.Context.MaxAnswerChars, "max_answer_chars").ApplyDefault(2000)
		figureout.Value(s, &c.Context.GatewayURL, "gateway_url",
			figureout.MovedFrom("context.ssh_mcp_url")).ApplyDefault("")
		figureout.Value(s, &c.Context.GatewayHeaders, "gateway_headers",
			figureout.MovedFrom("context.ssh_mcp_headers")).ApplyDefault(nil)
		figureout.Value(s, &c.Context.SandboxMachine, "sandbox_machine").ApplyDefault("sandbox")
		figureout.Value(s, &c.Context.PreSearch, "pre_search").ApplyDefault(true)
		figureout.Value(s, &c.Context.PreSearchLimit, "pre_search_limit").ApplyDefault(12)
		figureout.Value(s, &c.Context.ShowDebugInfo, "show_debug_info").ApplyDefault(false)
	})
}

// describeAlertmanager declares the alert webhook and what a firing alert does.
func describeAlertmanager(c *wire, s *figureout.Schema[wire]) {
	figureout.Group(s, "alertmanager", func(s *figureout.Schema[wire]) {
		figureout.Group(s, "webhook", func(s *figureout.Schema[wire]) {
			figureout.Value(s, &c.Alertmanager.WebhookEnabled, "enabled").ApplyDefault(false)
			secret(s, &c.Secrets.AlertmanagerWebhookTk, "token").
				Doc("Alertmanager signs nothing; this bearer token is the only auth")
		})
		figureout.Group(s, "notify", func(s *figureout.Schema[wire]) {
			figureout.Value(s, &c.Alertmanager.Notify, "enabled").ApplyDefault(true).
				Doc("an alert nobody is told about is an alert that did nothing")
		})
		figureout.Group(s, "investigate", func(s *figureout.Schema[wire]) {
			figureout.Value(s, &c.Alertmanager.Investigate, "enabled").ApplyDefault(false).
				Doc("off by default: it spends LLM budget per alert")
			figureout.Value(s, &c.Alertmanager.InvestigateMinSeverity, "min_severity").ApplyDefault("").
				Doc("info, warning or critical; empty means no floor")
		})
	})
	figureout.Ignore(s, &c.Alertmanager.WebhookToken,
		figureout.Reason("materialized from alertmanager.webhook.token"))
}

// describeIngest declares the ssingest daemon and its indexing workers.
func describeIngest(c *wire, s *figureout.Schema[wire]) {
	figureout.Group(s, "ingest", func(s *figureout.Schema[wire]) {
		figureout.Value(s, &c.Ingest.Addr, "addr").ApplyDefault(defaultIngestAddr)
		pollGroup(s, "git", &c.Ingest.GitPollIntervalSeconds)
		pollGroup(s, "files", &c.Ingest.FilesPollIntervalSeconds)
		pollGroup(s, "telegram", &c.Ingest.TelegramPollIntervalSeconds)

		figureout.Group(s, "worker", func(s *figureout.Schema[wire]) {
			figureout.Value(s, &c.Ingest.Worker.Enabled, "enabled").ApplyDefault(true).
				Doc("runs the drain loop inside ssingest serve; turn off once ssingest worker is deployed")
			figureout.Value(s, &c.Ingest.Worker.Concurrency, "concurrency").
				ApplyDefault(defaultIngestWorkerConcurrency)
			figureout.Value(s, &c.Ingest.Worker.LeaseSeconds, "lease_seconds").
				ApplyDefault(defaultIngestWorkerLeaseSeconds).
				Doc("also the handler deadline; must exceed the slowest embed-and-upsert")
			figureout.Value(s, &c.Ingest.Worker.MaxAttempts, "max_attempts").
				ApplyDefault(defaultIngestWorkerMaxAttempts)
			figureout.Value(s, &c.Ingest.Worker.PollIntervalSeconds, "poll_interval_seconds").
				ApplyDefault(defaultIngestWorkerPollSeconds)
		})
	})
}

// pollGroup declares a "<name>: {poll: {interval_seconds: N}}" block.
func pollGroup(s *figureout.Schema[wire], name string, field *int) {
	figureout.Group(s, name, func(s *figureout.Schema[wire]) {
		figureout.Group(s, "poll", func(s *figureout.Schema[wire]) {
			figureout.Value(s, field, "interval_seconds").ApplyDefault(0)
		})
	})
}

// describeNotify declares notification delivery and the identity table.
func describeNotify(c *wire, s *figureout.Schema[wire]) {
	figureout.Group(s, "notify", func(s *figureout.Schema[wire]) {
		figureout.Group(s, "poll", func(s *figureout.Schema[wire]) {
			figureout.Value(s, &c.Notify.PollIntervalSeconds, "interval_seconds").ApplyDefault(0).
				Doc("how often ssbot drains the outbox; 0 disables delivery")
		})
		figureout.Value(s, &c.Notify.MaxAssignmentAgeSeconds, "max_assignment_age_seconds").
			ApplyDefault(0).
			Doc("0 uses notify.DefaultMaxAssignmentAge (24h); negative disables the check")
		figureout.ListOf(s, &c.Notify.Identities, "identities", func(e *NotifyIdentity, s *figureout.Schema[NotifyIdentity]) {
			figureout.Value(s, &e.TelegramUserID, "telegram_id").GreaterThan(0).
				Doc("an identity with no Telegram id addresses nobody")
			figureout.Value(s, &e.GitLabUsername, "gitlab").ApplyDefault("")
			figureout.Value(s, &e.JiraAccountID, "jira").ApplyDefault("")
			figureout.Value(s, &e.JiraDisplayName, "jira_display").ApplyDefault("")
		}).MergeByKey("telegram_id")

		figureout.Invariant(s, "identity-addresses-someone", func(c *wire) error {
			for i, id := range c.Notify.Identities {
				if id.GitLabUsername == "" && id.JiraAccountID == "" {
					return figureout.At(identityPath(i)).Errorf("set gitlab, jira, or both")
				}
			}
			return nil
		})
	})
}

// describeProxies declares the per-client HTTP proxies.
func describeProxies(c *wire, s *figureout.Schema[wire]) {
	figureout.Group(s, "proxies", func(s *figureout.Schema[wire]) {
		secret(s, &c.Secrets.ProxyFetch, "fetch")
		secret(s, &c.Secrets.ProxyGit, "git")
		secret(s, &c.Secrets.ProxyGitLab, "gitlab")
		secret(s, &c.Secrets.ProxyJira, "jira")
		secret(s, &c.Secrets.ProxyOllama, "ollama")
		secret(s, &c.Secrets.ProxyOpenRouter, "openrouter")
	})
	figureout.IgnoreRecursive(s, &c.Proxies, figureout.Reason("materialized from proxies.*"))
}

// describeFetch declares the URL fetcher's allowlist.
//
// The allowlist fails closed, so an unknown proxy name or a pattern without a
// scheme has to be an error rather than a silently dropped site.
func describeFetch(c *wire, s *figureout.Schema[wire]) {
	figureout.Group(s, "fetch", func(s *figureout.Schema[wire]) {
		figureout.ListOf(s, &c.Fetch.Sites, "sites", describeFetchSite).MergeByKey("name")
	})

	figureout.Invariant(s, "fetch-site-proxy-exists", func(c *wire) error {
		for i, site := range c.Fetch.Sites {
			if site.Proxy == "" {
				continue
			}
			if fetchProxySecret(&c.Secrets, site.Proxy) == nil {
				return figureout.At(sitePath(i, "proxy")).
					Errorf("fetch site %q references unknown or empty proxy %q", site.Name, site.Proxy)
			}
		}
		return nil
	})
	figureout.Invariant(s, "fetch-site-names-unique", func(c *wire) error {
		seen := make(map[string]struct{}, len(c.Fetch.Sites))
		for i, site := range c.Fetch.Sites {
			if _, ok := seen[site.Name]; ok {
				return figureout.At(sitePath(i, "name")).Errorf("duplicate fetch site %q", site.Name)
			}
			seen[site.Name] = struct{}{}
		}
		return nil
	})
}

func describeFetchSite(e *FetchSite, s *figureout.Schema[FetchSite]) {
	figureout.Value(s, &e.Name, "name").NonEmpty()
	figureout.Value(s, &e.URLPatterns, "url_patterns").MinItems(1).
		Doc("doublestar globs; each must start with http:// or https://")
	figureout.Value(s, &e.Methods, "methods").ApplyDefault(nil).
		Doc("empty means GET only")
	figureout.Value(s, &e.Proxy, "proxy").ApplyDefault("").
		Doc("a name from proxies.*")
	figureout.Value(s, &e.MaxBytes, "max_bytes").ApplyDefault(int64(0))
	figureout.Value(s, &e.Timeout, "timeout").ApplyDefault(time.Duration(0))
	figureout.ObjectFunc(s, &e.Credentials, "credentials", func(e *FetchCredentials, s *figureout.Schema[FetchCredentials]) {
		figureout.Value(s, &e.Type, "type").ApplyDefault("").
			Doc("none, bearer, header or basic")
		figureout.Value(s, &e.TokenEnv, "token_env").ApplyDefault("")
		figureout.Value(s, &e.Header, "header").ApplyDefault("")
		figureout.Value(s, &e.Username, "username").ApplyDefault("")
		figureout.Value(s, &e.PasswordEnv, "password_env").ApplyDefault("")
		figureout.Ignore(s, &e.Token, figureout.Reason("read from token_env after resolve"))
		figureout.Ignore(s, &e.Password, figureout.Reason("read from password_env after resolve"))
	})
}
