// Package config loads sisyphus configuration from YAML.
package config

import (
	"time"

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

	Agent        AgentConfig
	Alertmanager AlertmanagerConfig
	Context      ContextConfig
	Ingest       IngestConfig
	Notify       NotifyConfig

	// Warnings holds deprecation warnings collected while resolving the
	// config (e.g. use of a field superseded by a per-service section). The
	// caller should log these.
	Warnings []string
}

// AlertmanagerConfig configures the Alertmanager event source: the webhook
// endpoint `ssingest serve` exposes, and whether a firing alert should start
// an agent investigation.
//
// Alertmanager signs nothing, so WebhookToken (a shared bearer token) is the
// only authentication the endpoint can do. An enabled webhook with no token
// serves anyone who can reach `ingest.addr`.
type AlertmanagerConfig struct {
	WebhookEnabled bool
	WebhookToken   string

	// Notify announces every alert to the registered chats as it arrives,
	// independently of Investigate. On by default: an alert nobody is told
	// about is an alert that did nothing.
	Notify bool

	// Investigate submits an agent investigation for each qualifying firing
	// alert. Off by default: it spends LLM budget per alert.
	Investigate bool
	// InvestigateMinSeverity drops alerts below this severity ("info",
	// "warning", "critical"). Empty means no floor.
	InvestigateMinSeverity string
}

// NotifyConfig controls the delivery half of the notification gateway:
// PollIntervalSeconds is how often ssbot drains the outbox and pushes pending
// notifications to Telegram. 0 (the default) disables the drain loop.
//
// It no longer schedules any fetching: the events notifications are built from
// are emitted by the GitLab and Jira ingestion runs, on those sources' own poll
// intervals.
type NotifyConfig struct {
	PollIntervalSeconds int

	// MaxAssignmentAgeSeconds is how old an assignment (or review request)
	// may be and still notify. Source events state *current* membership, not
	// a change to it, so without a cutoff any edit to a long-assigned issue
	// re-announces that assignment — which is what a new outbox, or a user
	// who just got an identity, sees a burst of.
	//
	// 0 uses notify.DefaultMaxAssignmentAge (24h); negative disables the
	// check. An assignment whose timestamp the source did not report always
	// notifies: over-notifying costs one message that dedup would collapse
	// anyway, under-notifying loses it silently.
	MaxAssignmentAgeSeconds int

	// Identities maps Telegram users to the GitLab/Jira identities events are
	// addressed to. It is the *only* way that mapping is established: a bot
	// user cannot claim an identity, because nothing they type proves they own
	// it — anyone could name a colleague's account and receive their
	// notifications. ssapi reconciles this list into the database on startup.
	Identities []NotifyIdentity
}

// NotifyIdentity is one configured Telegram to GitLab/Jira mapping.
type NotifyIdentity struct {
	TelegramUserID  int64
	GitLabUsername  string
	JiraAccountID   string
	JiraDisplayName string
}

// IngestConfig configures ssingest's `serve` daemon mode: the address its
// health/webhook HTTP server listens on, and poll intervals for the sources
// that have no dedicated config section of their own to hold one (GitLab and
// Jira already carry their own Poll.IntervalSeconds/Webhook fields, reused
// as-is by `serve`).
type IngestConfig struct {
	Addr                        string
	GitPollIntervalSeconds      int
	FilesPollIntervalSeconds    int
	TelegramPollIntervalSeconds int
	Worker                      IngestWorkerConfig
}

// IngestWorkerConfig controls the indexing half of ingestion: the queue
// workers that chunk, embed and upsert documents `ssingest serve` publishes.
//
// Fetching is single-owner (it advances cursors and holds source credentials);
// indexing is idempotent on (source, source_id) and scales with replicas, which
// is why `ssingest worker` exists as a separate deployment.
type IngestWorkerConfig struct {
	// Enabled runs the drain loop inside `ssingest serve` as well.
	//
	// It defaults to true so a single-pod install keeps indexing what it
	// publishes with no extra configuration. Turn it off once dedicated
	// `ssingest worker` replicas are deployed, so the scheduler pod is not
	// also competing for embedding capacity.
	Enabled bool
	// Concurrency is how many documents one worker process indexes at once.
	Concurrency int
	// LeaseSeconds bounds one document's indexing. It is also the handler's
	// deadline (see queue.Delivery.Deadline), so it must comfortably exceed
	// the slowest embed-and-upsert, or a large document is reclaimed mid-run
	// and retried forever.
	LeaseSeconds int
	// MaxAttempts is the attempt budget per document before the job is left
	// in the queue's terminal status for inspection.
	MaxAttempts int
	// PollIntervalSeconds is how long a worker waits after finding no work.
	PollIntervalSeconds int
}

// Lease is LeaseSeconds as a duration.
func (c IngestWorkerConfig) Lease() time.Duration {
	return time.Duration(c.LeaseSeconds) * time.Second
}

// PollInterval is PollIntervalSeconds as a duration.
func (c IngestWorkerConfig) PollInterval() time.Duration {
	return time.Duration(c.PollIntervalSeconds) * time.Second
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
	MaxConcurrent         int
	MaxBodyBytes          int64
	// ShowDebugInfo attaches agent-loop diagnostics (trace ID, duration,
	// tool calls, token usage) to Report.Debug, for operators debugging
	// /investigate. Off by default.
	ShowDebugInfo bool
}

// ContextConfig holds configuration for the agentic /context workflow.
type ContextConfig struct {
	Agentic        bool
	MaxIterations  int
	TimeoutSeconds int
	MaxAnswerChars int
	GatewayURL     string
	GatewayHeaders map[string]string
	SandboxMachine string
	PreSearch      bool
	PreSearchLimit int
	// ShowDebugInfo attaches agent-loop diagnostics (trace ID, duration,
	// tool calls, token usage) to Answer.Debug, for operators debugging
	// /context. Off by default.
	ShowDebugInfo bool
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
	// ReasoningEffort requests OpenRouter's unified reasoning mode
	// ("low", "medium", "high"); empty leaves it unset, so whether a
	// completion carries reasoning is entirely up to whichever provider
	// OpenRouter happens to route the request to. Validated against
	// validReasoningEfforts.
	ReasoningEffort string
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
	// AnswerTimeoutSeconds bounds how long ssbot waits for a /context answer
	// before giving up on it. It is the binding ceiling for a Telegram user:
	// context.timeout_seconds bounds the answerer, but ssbot stops waiting
	// first, so raising that alone changes nothing here.
	AnswerTimeoutSeconds int
}

// TelegramChat describes one Telegram chat to monitor.
type TelegramChat struct {
	ID       int64  `yaml:"id"`
	Username string `yaml:"username"`
	Limit    int    `yaml:"limit"`
}

const (
	// defaultIngestWorkerConcurrency matches ingestrun's in-process indexing
	// fan-out, so moving indexing onto the queue does not change how hard a
	// single process leans on the embedder.
	defaultIngestWorkerConcurrency = 8
	// defaultIngestWorkerLeaseSeconds must cover the slowest single document:
	// a large file chunked into hundreds of pieces, each embedded over HTTP.
	defaultIngestWorkerLeaseSeconds = 600
	defaultIngestWorkerMaxAttempts  = 3
	defaultIngestWorkerPollSeconds  = 1
)

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

// ProxyConfig configures per-client HTTP proxies.
type ProxyConfig struct {
	Fetch      string
	Git        string
	GitLab     string
	Jira       string
	Ollama     string
	OpenRouter string
}

// GitConfig configures git repository content + commit ingestion.
type GitConfig struct {
	WorkDir string      `yaml:"work_dir"`
	Token   string      `yaml:"-"`
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

// Default addresses for each service's own HTTP server. Kept as constants so
// resolve() can tell a deprecated top-level field apart from a per-service
// section the user actually configured.
const (
	defaultHTTPAddr   = ":8080"
	defaultMCPAddr    = ":8081"
	defaultBotAddr    = ":8083"
	defaultIngestAddr = ":8084"
)

// LogWarnings logs any deprecation warnings collected while resolving the
// config. Call once after Load.
func (c Config) LogWarnings(lg *zap.Logger) {
	for _, w := range c.Warnings {
		lg.Warn(w)
	}
}
