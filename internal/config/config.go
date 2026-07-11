// Package config loads sisyphus configuration from YAML.
package config

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/yaml"
	"go.uber.org/zap"
)

// Config holds all runtime configuration.
type Config struct {
	DatabaseDSN string

	QdrantAddr       string
	QdrantCollection string

	OllamaURL     string
	EmbedProvider string
	EmbedModel    string
	EmbedDim      int

	Git          GitConfig    // git repository content + commits
	GitLab       GitLabConfig // GitLab REST API: issues, MRs, releases
	ContextFiles []ContextFileSource

	Jira JiraConfig

	API        APIConfig
	MCP        MCPConfig
	OpenRouter OpenRouter
	Telegram   Telegram
	Proxies    ProxyConfig
	Fetch      FetchConfig

	Agent   AgentConfig
	Context ContextConfig

	// Warnings holds deprecation warnings collected while resolving the
	// config (e.g. use of a field superseded by a per-service section). The
	// caller should log these.
	Warnings []string
}

// MCPConfig configures the ssmcp service: the address its Streamable HTTP
// server listens on, and the bearer token it optionally enforces on /mcp.
type MCPConfig struct {
	Addr      string
	AuthToken string
}

// AgentConfig holds configuration for the ssagent service.
type AgentConfig struct {
	Addr                  string
	BaseURL               string
	AuthToken             string
	Model                 string
	MaxToolIterations     int
	RequestTimeoutSeconds int
	GatewayURL            string
	MaxReportChars        int
}

// ContextConfig holds configuration for the agentic /context workflow.
type ContextConfig struct {
	Agentic        bool
	MaxIterations  int
	TimeoutSeconds int
	MaxAnswerChars int
	SSHMCPURL      string
	SSHMCPHeaders  map[string]string
	SandboxMachine string
	PreSearch      bool
	PreSearchLimit int
}

// JiraConfig holds Jira REST API configuration for ingestion.
type JiraConfig struct {
	BaseURL  string
	Email    string
	Username string
	APIToken string
	Password string
	PAT      string
	Projects []JiraProject

	WebhookSecret  string
	WebhookEnabled bool

	// PollIntervalSeconds, if > 0, runs incremental Jira ingestion on a timer
	// in addition to (or instead of) webhooks. 0 disables polling.
	PollIntervalSeconds int
}

// JiraProject describes one Jira project to ingest.
type JiraProject struct {
	Key string `yaml:"key"`
}

// APIConfig configures the HTTP API: the address ssapi's own server listens
// on, the token it enforces, and (for ssbot/ssmcp/ssagent) the base URL of
// the ssapi instance to call.
type APIConfig struct {
	HTTPAddr  string
	BaseURL   string
	AuthToken string
}

// OpenRouter holds configuration for the OpenRouter LLM API.
type OpenRouter struct {
	APIKey string
	Model  string
}

// Enabled reports whether OpenRouter is configured.
func (o OpenRouter) Enabled() bool { return o.APIKey != "" }

// Telegram holds gotd auth configuration (plan: user session + bot).
type Telegram struct {
	// Addr is the address ssbot's standalone health/ready HTTP server
	// listens on. ssbot has no primary HTTP API of its own to attach health
	// checks to (unlike ssapi/ssmcp/ssagent), so it needs its own address.
	Addr           string
	AppID          int
	AppHash        string
	BotToken       string
	SessionDir     string
	MonitorChats   []TelegramChat
	IngestSession  string
	Silent         bool
	AllowedChats   []int64
	AllowedUserIDs []int64
}

// TelegramChat describes one Telegram chat to monitor.
type TelegramChat struct {
	ID       int64  `yaml:"id"`
	Username string `yaml:"username"`
	Limit    int    `yaml:"limit"`
}

type fileConfig struct {
	// Deprecated: use api.http_addr.
	HTTPAddr    string `yaml:"http_addr"`
	DatabaseDSN Secret `yaml:"database_dsn"`

	QdrantAddr       string `yaml:"qdrant_addr"`
	QdrantCollection string `yaml:"qdrant_collection"`

	OllamaURL     string `yaml:"ollama_url"`
	EmbedProvider string `yaml:"embed_provider"`
	EmbedModel    string `yaml:"embed_model"`
	EmbedDim      int    `yaml:"embed_dim"`

	Git          fileGitConfig       `yaml:"git"`
	GitLab       fileGitLabConfig    `yaml:"gitlab"`
	ContextFiles []ContextFileSource `yaml:"context_files"`

	Jira fileJiraConfig `yaml:"jira"`

	API        fileAPIConfig   `yaml:"api"`
	MCP        fileMCPConfig   `yaml:"mcp"`
	OpenRouter fileOpenRouter  `yaml:"openrouter"`
	Telegram   fileTelegram    `yaml:"telegram"`
	Proxies    fileProxyConfig `yaml:"proxies"`
	Fetch      fileFetchConfig `yaml:"fetch"`

	// Deprecated: use mcp.addr.
	MCPAddr string `yaml:"mcp_addr"`
	// Deprecated: use mcp.auth_token.
	MCPAuthToken Secret `yaml:"mcp_auth_token"`

	Agent   fileAgentConfig   `yaml:"agent"`
	Context fileContextConfig `yaml:"context"`
}

type fileMCPConfig struct {
	Addr      string `yaml:"addr"`
	AuthToken Secret `yaml:"auth_token"`
}

type fileAgentConfig struct {
	Addr                  string `yaml:"addr"`
	BaseURL               string `yaml:"base_url"`
	AuthToken             Secret `yaml:"auth_token"`
	Model                 string `yaml:"model"`
	MaxToolIterations     int    `yaml:"max_tool_iterations"`
	RequestTimeoutSeconds int    `yaml:"request_timeout_seconds"`
	GatewayURL            string `yaml:"gateway_url"`
	MaxReportChars        int    `yaml:"max_report_chars"`
}

type fileContextConfig struct {
	Agentic        bool              `yaml:"agentic"`
	MaxIterations  int               `yaml:"max_iterations"`
	TimeoutSeconds int               `yaml:"timeout_seconds"`
	MaxAnswerChars int               `yaml:"max_answer_chars"`
	SSHMCPURL      string            `yaml:"ssh_mcp_url"`
	SSHMCPHeaders  map[string]string `yaml:"ssh_mcp_headers"`
	SandboxMachine string            `yaml:"sandbox_machine"`
	PreSearch      bool              `yaml:"pre_search"`
	PreSearchLimit int               `yaml:"pre_search_limit"`
}

// FetchConfig configures the URL fetcher allowlist.
type FetchConfig struct {
	Sites []FetchSite `yaml:"sites"`
}

// FetchSite defines one whitelisted site the agent may fetch URLs from.
type FetchSite struct {
	Name        string           `yaml:"name"`
	URLPatterns []string         `yaml:"url_patterns"`
	Methods     []string         `yaml:"methods"`
	Proxy       string           `yaml:"proxy"`
	Credentials FetchCredentials `yaml:"credentials"`
	MaxBytes    int64            `yaml:"max_bytes"`
	Timeout     time.Duration    `yaml:"timeout"`
}

// FetchCredentials specifies how to authenticate to a whitelisted site.
type FetchCredentials struct {
	Type        string `yaml:"type"`
	TokenEnv    string `yaml:"token_env"`
	Header      string `yaml:"header"`
	Username    string `yaml:"username"`
	PasswordEnv string `yaml:"password_env"`

	Token    string `yaml:"-"`
	Password string `yaml:"-"`
}

type fileFetchConfig struct {
	Sites []FetchSite `yaml:"sites"`
}

// ProxyConfig configures per-client HTTP proxies.
type ProxyConfig struct {
	Fetch      string
	Git        string
	GitLab     string
	Jira       string
	Ollama     string
	OpenRouter string
}

type fileProxyConfig struct {
	Fetch      Secret `yaml:"fetch"`
	Git        Secret `yaml:"git"`
	GitLab     Secret `yaml:"gitlab"`
	Jira       Secret `yaml:"jira"`
	Ollama     Secret `yaml:"ollama"`
	OpenRouter Secret `yaml:"openrouter"`
}

type fileJiraConfig struct {
	BaseURL  string        `yaml:"base_url"`
	Email    string        `yaml:"email"`
	Username Secret        `yaml:"username"`
	APIToken Secret        `yaml:"api_token"`
	Password Secret        `yaml:"password"`
	PAT      Secret        `yaml:"pat"`
	Projects []JiraProject `yaml:"projects"`

	Webhook struct {
		Enabled bool   `yaml:"enabled"`
		Secret  Secret `yaml:"secret"`
	} `yaml:"webhook"`

	// Poll runs incremental ingestion on a timer, as a supplement or
	// fallback to webhooks. IntervalSeconds <= 0 disables polling.
	Poll struct {
		IntervalSeconds int `yaml:"interval_seconds"`
	} `yaml:"poll"`
}

type fileAPIConfig struct {
	HTTPAddr  string `yaml:"http_addr"`
	BaseURL   string `yaml:"base_url"`
	AuthToken Secret `yaml:"auth_token"`
}

// GitConfig configures git repository content + commit ingestion.
type GitConfig struct {
	WorkDir string      `yaml:"work_dir"`
	Token   string      `yaml:"-"`
	Repos   []GitSource `yaml:"repos"`
}

type fileGitConfig struct {
	WorkDir string      `yaml:"work_dir"`
	Token   Secret      `yaml:"token"`
	Repos   []GitSource `yaml:"repos"`
}

// ContextFileSource describes a named set of local files to index as extra context.
type ContextFileSource struct {
	Name      string   `yaml:"name"`
	Root      string   `yaml:"root"`
	BaseURL   string   `yaml:"base_url"`
	Include   []string `yaml:"include"`
	Exclude   []string `yaml:"exclude"`
	Authority string   `yaml:"authority"`
}

// GitSource describes a git repository to ingest (content + optional commits).
type GitSource struct {
	Root    string `yaml:"root"`
	URL     string `yaml:"url"`
	Repo    string `yaml:"repo"`
	Branch  string `yaml:"branch"`
	BaseURL string `yaml:"base_url"`
	// Include/Exclude are doublestar globs applied at the walk stage, on top of
	// the built-in default skiplist. Empty Include means "all matched files".
	Include []string `yaml:"include"`
	Exclude []string `yaml:"exclude"`
	// Commits enables ingestion of commit messages on the default branch.
	Commits bool `yaml:"commits"`
	// Tags enables ingestion of git tags.
	Tags bool `yaml:"tags"`
	// Manifests enables ingestion of YAML manifests.
	Manifests bool `yaml:"manifests"`
	// Code enables ingestion of source code files (Go/TS/proto/SQL).
	Code bool `yaml:"code"`
	// ManifestExclude are additional excludes applied only when walking manifests.
	ManifestExclude []string `yaml:"manifest_exclude,omitempty"`
	// CodeInclude restricts code-walk to paths matching these globs.
	CodeInclude []string `yaml:"code_include,omitempty"`
	// CodeExclude skips code files matching these globs.
	CodeExclude []string `yaml:"code_exclude,omitempty"`
}

// GitLabConfig configures GitLab REST API ingestion (issues, MRs, releases).
type GitLabConfig struct {
	BaseURL       string
	Token         string
	Projects      []GitLabProject
	Issues        bool
	MergeRequests bool
	Releases      bool

	WebhookSecret  string
	WebhookEnabled bool

	// PollIntervalSeconds, if > 0, runs incremental GitLab ingestion on a
	// timer in addition to (or instead of) webhooks. 0 disables polling.
	PollIntervalSeconds int
}

// GitLabProject describes one GitLab project to ingest by numeric ID or path.
type GitLabProject struct {
	Ref string `yaml:"ref"`
}

type fileGitLabConfig struct {
	BaseURL       string          `yaml:"base_url"`
	Token         Secret          `yaml:"token"`
	Projects      []GitLabProject `yaml:"projects"`
	Issues        bool            `yaml:"issues"`
	MergeRequests bool            `yaml:"merge_requests"`
	Releases      bool            `yaml:"releases"`

	Webhook struct {
		Enabled bool   `yaml:"enabled"`
		Secret  Secret `yaml:"secret"`
	} `yaml:"webhook"`

	// Poll runs incremental ingestion on a timer, as a supplement or
	// fallback to webhooks (e.g. GitLab instance can't reach us, or webhooks
	// aren't configured). IntervalSeconds <= 0 disables polling.
	Poll struct {
		IntervalSeconds int `yaml:"interval_seconds"`
	} `yaml:"poll"`
}

type fileOpenRouter struct {
	APIKey Secret `yaml:"api_key"`
	Model  string `yaml:"model"`
}

type fileTelegram struct {
	Addr           string         `yaml:"addr"`
	AppID          int            `yaml:"app_id"`
	AppHash        Secret         `yaml:"app_hash"`
	BotToken       Secret         `yaml:"bot_token"`
	SessionDir     string         `yaml:"session_dir"`
	Silent         bool           `yaml:"silent"`
	MonitorChats   []TelegramChat `yaml:"monitor_chats"`
	IngestSession  string         `yaml:"ingest_session"`
	AllowedChats   []int64        `yaml:"allowed_chats"`
	AllowedUserIDs []int64        `yaml:"allowed_user_ids"`
}

// Secret describes a secret loaded from a literal value, environment variable,
// or file. Scalar YAML values are treated as literal values.
type Secret struct {
	Value string `yaml:"value"`
	Env   string `yaml:"env"`
	File  string `yaml:"file"`
}

// UnmarshalYAML accepts either a scalar secret value or a secret reference.
func (s *Secret) UnmarshalYAML(value *yaml.Node) error {
	var plain string
	if err := value.Decode(&plain); err == nil {
		s.Value = plain
		return nil
	}

	type secret Secret
	var ref secret
	if err := value.Decode(&ref); err != nil {
		return err
	}
	*s = Secret(ref)
	return nil
}

// LogWarnings logs any deprecation warnings collected while resolving the
// config. Call once after Load.
func (c Config) LogWarnings(lg *zap.Logger) {
	for _, w := range c.Warnings {
		lg.Warn(w)
	}
}

// Load reads configuration from YAML. Set SISYPHUS_CONFIG to choose the config
// file path; otherwise ./config.yaml is used when it exists.
func Load() (Config, error) {
	fc := defaultConfig()
	baseDir := "."

	if path := configPath(); path != "" {
		if err := loadFile(path, &fc); err != nil {
			return Config{}, err
		}
		baseDir = filepath.Dir(path)
	}

	c, err := fc.resolve(baseDir)
	if err != nil {
		return Config{}, err
	}
	if c.DatabaseDSN == "" {
		return Config{}, errors.New("database_dsn is required")
	}
	return c, nil
}

// Default addresses for each service's own HTTP server. Kept as constants so
// resolve() can tell a deprecated top-level field apart from a per-service
// section the user actually configured.
const (
	defaultHTTPAddr = ":8080"
	defaultMCPAddr  = ":8081"
	defaultBotAddr  = ":8083"
)

func defaultConfig() fileConfig {
	return fileConfig{
		QdrantAddr:       "localhost:6334",
		QdrantCollection: "corp_chunks",
		OllamaURL:        "http://localhost:11434",
		EmbedProvider:    "ollama",
		EmbedModel:       "bge-m3",
		EmbedDim:         1024,
		API: fileAPIConfig{
			HTTPAddr: defaultHTTPAddr,
			BaseURL:  "http://localhost:8080",
		},
		MCP: fileMCPConfig{
			Addr: defaultMCPAddr,
		},
		OpenRouter: fileOpenRouter{
			Model: "openai/gpt-4o-mini",
		},
		Telegram: fileTelegram{
			Addr:       defaultBotAddr,
			SessionDir: "./session",
		},
		Agent: fileAgentConfig{
			Addr:                  ":8082",
			MaxToolIterations:     8,
			RequestTimeoutSeconds: 180,
			MaxReportChars:        1500,
		},
		Context: fileContextConfig{
			Agentic:        false,
			MaxIterations:  6,
			TimeoutSeconds: 180,
			MaxAnswerChars: 2000,
			SandboxMachine: "sandbox",
			PreSearch:      true,
			PreSearchLimit: 12,
		},
	}
}

func configPath() string {
	if path := os.Getenv("SISYPHUS_CONFIG"); path != "" {
		return path
	}
	if _, err := os.Stat("config.yaml"); err == nil {
		return "config.yaml"
	}
	return ""
}

func loadFile(path string, c *fileConfig) error {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return errors.Wrap(err, "read config file")
	}
	if err := yaml.Unmarshal(data, c); err != nil {
		return errors.Wrap(err, "parse config file")
	}
	return nil
}

func (c fileConfig) resolve(baseDir string) (Config, error) {
	var warnings []string

	httpAddr, err := resolveDeprecatedAddr(&warnings, "http_addr", c.HTTPAddr, "api.http_addr", c.API.HTTPAddr, defaultHTTPAddr)
	if err != nil {
		return Config{}, err
	}
	mcpAddr, err := resolveDeprecatedAddr(&warnings, "mcp_addr", c.MCPAddr, "mcp.addr", c.MCP.Addr, defaultMCPAddr)
	if err != nil {
		return Config{}, err
	}
	mcpAuthTokenSecret, err := resolveDeprecatedSecret(&warnings, "mcp_auth_token", c.MCPAuthToken, "mcp.auth_token", c.MCP.AuthToken)
	if err != nil {
		return Config{}, err
	}

	dsn, err := c.DatabaseDSN.Resolve(baseDir)
	if err != nil {
		return Config{}, errors.Wrap(err, "database_dsn")
	}
	apiAuthToken, err := c.API.AuthToken.Resolve(baseDir)
	if err != nil {
		return Config{}, errors.Wrap(err, "api auth_token")
	}
	jiraUsername, err := c.Jira.Username.Resolve(baseDir)
	if err != nil {
		return Config{}, errors.Wrap(err, "jira username")
	}
	jiraToken, err := c.Jira.APIToken.Resolve(baseDir)
	if err != nil {
		return Config{}, errors.Wrap(err, "jira api_token")
	}
	jiraPassword, err := c.Jira.Password.Resolve(baseDir)
	if err != nil {
		return Config{}, errors.Wrap(err, "jira password")
	}
	jiraPAT, err := c.Jira.PAT.Resolve(baseDir)
	if err != nil {
		return Config{}, errors.Wrap(err, "jira pat")
	}
	jiraWebhookSecret, err := c.Jira.Webhook.Secret.Resolve(baseDir)
	if err != nil {
		return Config{}, errors.Wrap(err, "jira webhook secret")
	}
	gitToken, err := c.Git.Token.Resolve(baseDir)
	if err != nil {
		return Config{}, errors.Wrap(err, "git token")
	}
	gitlabToken, err := c.GitLab.Token.Resolve(baseDir)
	if err != nil {
		return Config{}, errors.Wrap(err, "gitlab token")
	}
	gitlabWebhookSecret, err := c.GitLab.Webhook.Secret.Resolve(baseDir)
	if err != nil {
		return Config{}, errors.Wrap(err, "gitlab webhook secret")
	}
	openRouterKey, err := c.OpenRouter.APIKey.Resolve(baseDir)
	if err != nil {
		return Config{}, errors.Wrap(err, "openrouter api_key")
	}
	telegramAppHash, err := c.Telegram.AppHash.Resolve(baseDir)
	if err != nil {
		return Config{}, errors.Wrap(err, "telegram app_hash")
	}
	telegramBotToken, err := c.Telegram.BotToken.Resolve(baseDir)
	if err != nil {
		return Config{}, errors.Wrap(err, "telegram bot_token")
	}
	proxies, err := c.Proxies.resolve(baseDir)
	if err != nil {
		return Config{}, err
	}
	fetchConfig, err := c.Fetch.resolve(proxies, &warnings)
	if err != nil {
		return Config{}, err
	}
	mcpAuthToken, err := mcpAuthTokenSecret.Resolve(baseDir)
	if err != nil {
		return Config{}, errors.Wrap(err, "mcp auth_token")
	}
	agentAuthToken, err := c.Agent.AuthToken.Resolve(baseDir)
	if err != nil {
		return Config{}, errors.Wrap(err, "agent auth_token")
	}

	return Config{
		DatabaseDSN:      dsn,
		QdrantAddr:       c.QdrantAddr,
		QdrantCollection: c.QdrantCollection,
		OllamaURL:        c.OllamaURL,
		EmbedProvider:    c.EmbedProvider,
		EmbedModel:       c.EmbedModel,
		EmbedDim:         c.EmbedDim,
		Git: GitConfig{
			WorkDir: c.Git.WorkDir,
			Token:   gitToken,
			Repos:   c.Git.Repos,
		},
		ContextFiles: c.ContextFiles,
		GitLab: GitLabConfig{
			BaseURL:             c.GitLab.BaseURL,
			Token:               gitlabToken,
			Projects:            c.GitLab.Projects,
			Issues:              c.GitLab.Issues,
			MergeRequests:       c.GitLab.MergeRequests,
			Releases:            c.GitLab.Releases,
			WebhookSecret:       gitlabWebhookSecret,
			WebhookEnabled:      c.GitLab.Webhook.Enabled,
			PollIntervalSeconds: c.GitLab.Poll.IntervalSeconds,
		},
		Jira: JiraConfig{
			BaseURL:             c.Jira.BaseURL,
			Email:               c.Jira.Email,
			Username:            jiraUsername,
			APIToken:            jiraToken,
			Password:            jiraPassword,
			PAT:                 jiraPAT,
			Projects:            c.Jira.Projects,
			WebhookSecret:       jiraWebhookSecret,
			WebhookEnabled:      c.Jira.Webhook.Enabled,
			PollIntervalSeconds: c.Jira.Poll.IntervalSeconds,
		},
		API: APIConfig{
			HTTPAddr:  httpAddr,
			BaseURL:   c.API.BaseURL,
			AuthToken: apiAuthToken,
		},
		MCP: MCPConfig{
			Addr:      mcpAddr,
			AuthToken: mcpAuthToken,
		},
		OpenRouter: OpenRouter{
			APIKey: openRouterKey,
			Model:  c.OpenRouter.Model,
		},
		Telegram: Telegram{
			Addr:           c.Telegram.Addr,
			AppID:          c.Telegram.AppID,
			AppHash:        telegramAppHash,
			BotToken:       telegramBotToken,
			SessionDir:     c.Telegram.SessionDir,
			Silent:         c.Telegram.Silent,
			MonitorChats:   c.Telegram.MonitorChats,
			IngestSession:  c.Telegram.IngestSession,
			AllowedChats:   c.Telegram.AllowedChats,
			AllowedUserIDs: c.Telegram.AllowedUserIDs,
		},
		Proxies:  proxies,
		Fetch:    fetchConfig,
		Warnings: warnings,
		Agent: AgentConfig{
			Addr:                  c.Agent.Addr,
			BaseURL:               c.Agent.BaseURL,
			AuthToken:             agentAuthToken,
			Model:                 c.Agent.Model,
			MaxToolIterations:     c.Agent.MaxToolIterations,
			RequestTimeoutSeconds: c.Agent.RequestTimeoutSeconds,
			GatewayURL:            c.Agent.GatewayURL,
			MaxReportChars:        c.Agent.MaxReportChars,
		},
		Context: ContextConfig{
			Agentic:        c.Context.Agentic,
			MaxIterations:  c.Context.MaxIterations,
			TimeoutSeconds: c.Context.TimeoutSeconds,
			MaxAnswerChars: c.Context.MaxAnswerChars,
			SSHMCPURL:      c.Context.SSHMCPURL,
			SSHMCPHeaders:  c.Context.SSHMCPHeaders,
			SandboxMachine: c.Context.SandboxMachine,
			PreSearch:      c.Context.PreSearch,
			PreSearchLimit: c.Context.PreSearchLimit,
		},
	}, nil
}

func (c fileFetchConfig) resolve(proxies ProxyConfig, warnings *[]string) (FetchConfig, error) {
	seen := make(map[string]struct{}, len(c.Sites))
	sites := make([]FetchSite, 0, len(c.Sites))
	for _, site := range c.Sites {
		name := strings.TrimSpace(site.Name)
		if name == "" {
			return FetchConfig{}, errors.New("fetch site name is required")
		}
		if _, ok := seen[name]; ok {
			return FetchConfig{}, errors.Errorf("duplicate fetch site %q", name)
		}
		seen[name] = struct{}{}
		site.Name = name

		if len(site.URLPatterns) == 0 {
			return FetchConfig{}, errors.Errorf("fetch site %q needs at least one url_patterns entry", name)
		}
		for _, pattern := range site.URLPatterns {
			if !strings.HasPrefix(pattern, "https://") && !strings.HasPrefix(pattern, "http://") {
				return FetchConfig{}, errors.Errorf("fetch site %q pattern %q must start with http:// or https://", name, pattern)
			}
		}
		methods, warns, err := normalizeFetchMethods(site.Methods)
		if err != nil {
			return FetchConfig{}, errors.Wrap(err, "fetch site "+name)
		}
		for _, warn := range warns {
			*warnings = append(*warnings, "fetch site "+name+" allows write method "+warn+"; prefer read-only methods unless explicitly required")
		}
		site.Methods = methods

		if site.Proxy != "" && fetchProxyURL(proxies, site.Proxy) == "" {
			return FetchConfig{}, errors.Errorf("fetch site %q references unknown or empty proxy %q", name, site.Proxy)
		}
		creds, err := resolveFetchCredentials(site.Credentials)
		if err != nil {
			return FetchConfig{}, errors.Wrap(err, "fetch site "+name+" credentials")
		}
		site.Credentials = creds
		sites = append(sites, site)
	}
	return FetchConfig{Sites: sites}, nil
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

func fetchProxyURL(proxies ProxyConfig, name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "fetch":
		return proxies.Fetch
	case "git":
		return proxies.Git
	case "gitlab":
		return proxies.GitLab
	case "jira":
		return proxies.Jira
	case "ollama":
		return proxies.Ollama
	case "openrouter":
		return proxies.OpenRouter
	default:
		return ""
	}
}

// resolveDeprecatedAddr merges a deprecated top-level address field with its
// new per-service home. The deprecated field wins if set, and is recorded as
// a warning; if the new field was also explicitly changed from its default,
// that's an ambiguous configuration (the caller can't tell which one was
// meant) and is rejected.
func resolveDeprecatedAddr(warnings *[]string, deprecatedKey, deprecatedValue, newKey, newValue, newDefault string) (string, error) {
	if deprecatedValue == "" {
		return newValue, nil
	}
	if newValue != newDefault {
		return "", errors.Errorf("%s is deprecated in favor of %s; set only %s", deprecatedKey, newKey, newKey)
	}
	*warnings = append(*warnings, deprecatedKey+" is deprecated, use "+newKey+" instead")
	return deprecatedValue, nil
}

// resolveDeprecatedSecret is resolveDeprecatedAddr for Secret fields, which
// have no meaningful "default" to compare against: any explicit value in
// both places is a conflict.
func resolveDeprecatedSecret(warnings *[]string, deprecatedKey string, deprecated Secret, newKey string, current Secret) (Secret, error) {
	if deprecated == (Secret{}) {
		return current, nil
	}
	if current != (Secret{}) {
		return Secret{}, errors.Errorf("%s is deprecated in favor of %s; set only %s", deprecatedKey, newKey, newKey)
	}
	*warnings = append(*warnings, deprecatedKey+" is deprecated, use "+newKey+" instead")
	return deprecated, nil
}

func (c fileProxyConfig) resolve(baseDir string) (ProxyConfig, error) {
	fetch, err := c.Fetch.Resolve(baseDir)
	if err != nil {
		return ProxyConfig{}, errors.Wrap(err, "proxy fetch")
	}
	git, err := c.Git.Resolve(baseDir)
	if err != nil {
		return ProxyConfig{}, errors.Wrap(err, "proxy git")
	}
	gitlab, err := c.GitLab.Resolve(baseDir)
	if err != nil {
		return ProxyConfig{}, errors.Wrap(err, "proxy gitlab")
	}
	jira, err := c.Jira.Resolve(baseDir)
	if err != nil {
		return ProxyConfig{}, errors.Wrap(err, "proxy jira")
	}
	ollama, err := c.Ollama.Resolve(baseDir)
	if err != nil {
		return ProxyConfig{}, errors.Wrap(err, "proxy ollama")
	}
	openrouter, err := c.OpenRouter.Resolve(baseDir)
	if err != nil {
		return ProxyConfig{}, errors.Wrap(err, "proxy openrouter")
	}
	return ProxyConfig{
		Fetch:      fetch,
		Git:        git,
		GitLab:     gitlab,
		Jira:       jira,
		Ollama:     ollama,
		OpenRouter: openrouter,
	}, nil
}

// Resolve returns the configured secret value.
func (s Secret) Resolve(baseDir string) (string, error) {
	set := 0
	if s.Value != "" {
		set++
	}
	if s.Env != "" {
		set++
	}
	if s.File != "" {
		set++
	}
	if set == 0 {
		return "", nil
	}
	if set > 1 {
		return "", errors.New("set only one of value, env, or file")
	}
	if s.Value != "" {
		return s.Value, nil
	}
	if s.Env != "" {
		return os.Getenv(s.Env), nil
	}

	path := s.File
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", errors.Wrap(err, "read secret file")
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}
