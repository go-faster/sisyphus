// Package index defines the shared contract for ingestion, chunking, embedding
// and search. It is intentionally dependency-light (stdlib + google/uuid) so
// every other package can depend on it without import cycles.
package index

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Source identifies where a Document came from.
type Source string

const (
	// GitLab REST API sources (one GitLab instance; project in metadata/SourceID).
	SourceGitLabIssue   Source = "gitlab_issue"
	SourceGitLabMR      Source = "gitlab_mr"
	SourceGitLabRelease Source = "gitlab_release"

	SourceJira     Source = "jira"
	SourceTelegram Source = "telegram"
)

// Per-repo git source prefixes. The repo name is appended to build the concrete
// Source (so each repo gets its own SyncState row and can be reset independently).
const (
	SourceGitDocsPrefix      = "git_docs:"
	SourceGitCommitsPrefix   = "git_commits:"
	SourceGitTagsPrefix      = "git_tags:"
	SourceGitManifestPrefix  = "git_manifest:"
	SourceGitCodePrefix      = "git_code:"
	SourceContextFilesPrefix = "context_files:"
)

// SourceGitDocs returns the Source for git-walked content of the given repo.
func SourceGitDocs(repo string) Source { return Source(SourceGitDocsPrefix + repo) }

// SourceGitCommit returns the Source for commit messages of the given repo.
func SourceGitCommit(repo string) Source { return Source(SourceGitCommitsPrefix + repo) }

// SourceGitTag returns the Source for git tags of the given repo.
func SourceGitTag(repo string) Source { return Source(SourceGitTagsPrefix + repo) }

// SourceGitManifest returns the Source for YAML manifests of the given repo.
func SourceGitManifest(repo string) Source { return Source(SourceGitManifestPrefix + repo) }

// SourceGitCode returns the Source for source code files of the given repo.
func SourceGitCode(repo string) Source { return Source(SourceGitCodePrefix + repo) }

// SourceContextFiles returns the Source for an additional file set.
func SourceContextFiles(name string) Source { return Source(SourceContextFilesPrefix + name) }

// SourceMatchesPrefix reports whether a concrete source belongs to one of the
// requested source prefixes. Prefixes ending in ':' match per-repo sources;
// other prefixes match exact source names.
func SourceMatchesPrefix(source string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	for _, prefix := range prefixes {
		if strings.HasSuffix(prefix, ":") {
			if strings.HasPrefix(source, prefix) {
				return true
			}
			continue
		}
		if source == prefix {
			return true
		}
	}
	return false
}

// ChunkType classifies a Chunk so retrieval/ranking can treat them differently.
type ChunkType string

const (
	ChunkSection ChunkType = "section" // markdown heading section

	ChunkJiraSummary     ChunkType = "jira_issue_summary"
	ChunkJiraDescription ChunkType = "jira_issue_description"
	ChunkJiraComments    ChunkType = "jira_issue_comment_group"
	ChunkJiraResolution  ChunkType = "jira_issue_resolution"

	ChunkTelegramRequestSummary ChunkType = "telegram_request_summary"
	ChunkTelegramRawExcerpt     ChunkType = "telegram_raw_excerpt"

	ChunkGitCommit ChunkType = "git_commit"
	ChunkGitTag    ChunkType = "git_tag"

	ChunkGitLabIssueSummary  ChunkType = "gitlab_issue_summary"
	ChunkGitLabIssueComments ChunkType = "gitlab_issue_comment_group"
	ChunkGitLabMRSummary     ChunkType = "gitlab_mr_summary"
	ChunkGitLabMRComments    ChunkType = "gitlab_mr_comment_group"
	ChunkGitLabReleaseNotes  ChunkType = "gitlab_release_notes"

	ChunkCodeFile   ChunkType = "code_file"
	ChunkCodeSymbol ChunkType = "code_symbol"
	ChunkManifest   ChunkType = "manifest"
)

// Authority expresses how much to trust a source during ranking (plan §11).
type Authority string

const (
	AuthorityHigh       Authority = "high"
	AuthorityMediumHigh Authority = "medium_high"
	AuthorityMedium     Authority = "medium"
	AuthorityLowMedium  Authority = "low_medium"
	AuthorityLow        Authority = "low"
)

// Document is a normalized source artifact (plan §1).
type Document struct {
	ID        uuid.UUID
	Source    Source
	SourceID  string // stable id within the source (path, jira key, chat:msg)
	URL       string
	Title     string
	Body      string
	BodyHash  string // Hash(Body); set by the pipeline if empty
	Metadata  map[string]any
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Chunk is a retrievable unit derived from a Document (plan §1).
type Chunk struct {
	ID         uuid.UUID
	DocumentID uuid.UUID
	Index      int
	Type       ChunkType
	Title      string
	Text       string
	TextHash   string // Hash(Text); set by the pipeline if empty
	Metadata   map[string]any
	TokenCount int
}

// Chunker turns a Document into ordered Chunks. Implementations must be pure and
// deterministic: same Document in -> same Chunks out (so hashing is stable).
type Chunker interface {
	Chunk(ctx context.Context, doc Document) ([]Chunk, error)
}

// Embedder produces embedding vectors for texts. All returned vectors have Dim()
// length, in the same order as the input.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Dim() int
}

// Query is a retrieval request (plan §10).
type Query struct {
	Text           string
	Service        string            // optional service filter/boost
	Filters        map[string]string // optional payload filters (source, repo, jira_key, ...)
	SourceTier     string            // optional source policy name (curated, code, history, all)
	SourcePrefixes []string          // optional source prefixes to include
	Limit          int
}

// Result is a scored chunk returned by a Searcher.
type Result struct {
	Chunk Chunk
	Score float64
	// Vector is true if the score came from vector search, false for lexical.
	Vector bool
}

// Searcher retrieves candidate chunks for a Query. Both the Postgres FTS and the
// Qdrant vector backends implement this; retrieval merges their results.
type Searcher interface {
	Search(ctx context.Context, q Query) ([]Result, error)
}

// Summarizer produces a normalized summary for a piece of text (Telegram thread,
// Jira issue). The LLM backend is deferred; a stub implementation is used until a
// provider is wired.
type Summarizer interface {
	Summarize(ctx context.Context, prompt string) (string, error)
}

// Answerer constructs a final answer from retrieved context (plan §14).
type Answerer interface {
	Answer(ctx context.Context, q Query, results []Result) (Answer, error)
}

// Link is a labeled URL surfaced as an actionable link (e.g. a Telegram inline
// button, or a cited source). Text is the human-readable label; URL must be an
// absolute http(s) URL.
type Link struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

// Valid reports whether the link has a non-empty label and an absolute http(s)
// URL. Anything else (relative paths, other schemes, unparsable URLs, missing
// label) is rejected so that model-produced links can be filtered before they
// reach a user-facing surface.
func (l Link) Valid() bool {
	if strings.TrimSpace(l.Text) == "" {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(l.URL))
	if err != nil || u.Host == "" {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

// Answer is a structured answer: prose plus optional actionable links.
type Answer struct {
	Text  string
	Links []Link
	// Debug carries agent-loop diagnostics (trace ID, duration, tool calls,
	// token usage). Only populated when the operator has opted into debug
	// info via config (context.show_debug_info / agent.show_debug_info);
	// nil otherwise, so the JSON/wire shape is unaffected when disabled.
	Debug *Debug
}

// Debug carries agent-loop diagnostics for an answer/report, surfaced only
// when the operator has opted into it via a config-level toggle. It's meant
// for debugging, not end-user consumption.
type Debug struct {
	TraceID string `json:"trace_id,omitempty"`
	// DurationMS covers only the LLM tool-calling loop itself. For an async
	// job (e.g. /investigate) this excludes any time spent queued behind a
	// concurrency limit before the loop started — see QueuedMS/TotalMS for
	// the true end-to-end figure.
	DurationMS       int64 `json:"duration_ms,omitempty"`
	QueuedMS         int64 `json:"queued_ms,omitempty"`
	TotalMS          int64 `json:"total_ms,omitempty"`
	Iterations       int   `json:"iterations,omitempty"`
	ToolCalls        int   `json:"tool_calls,omitempty"`
	PromptTokens     int64 `json:"prompt_tokens,omitempty"`
	CompletionTokens int64 `json:"completion_tokens,omitempty"`
}

// Hash returns the hex sha256 of normalized text. Normalization trims surrounding
// whitespace and unifies line endings so cosmetic changes do not force re-embeds.
func Hash(text string) string {
	n := strings.ReplaceAll(text, "\r\n", "\n")
	n = strings.TrimSpace(n)
	sum := sha256.Sum256([]byte(n))
	return hex.EncodeToString(sum[:])
}

// NewID returns a new random UUID. Centralized so tests can swap it if needed.
func NewID() uuid.UUID { return uuid.New() }

// ContentRequest identifies a file to retrieve.
type ContentRequest struct {
	Repo   string // repo name (matches metadata "repo")
	Path   string // repo-relative path (matches metadata "path")
	Branch string // optional branch ref (matches metadata "branch")
	Start  int    // optional 1-indexed start line (inclusive)
	End    int    // optional 1-indexed end line (inclusive)
}

// ContentResponse holds the retrieved file content.
type ContentResponse struct {
	Content string
	Source  string // "local_clone" or "database"
	Found   bool
}

// ContentResolver retrieves actual file content from source repositories.
type ContentResolver interface {
	ResolveContent(ctx context.Context, req ContentRequest) (ContentResponse, error)
}

var (
	// ErrURLNotAllowed reports that a URL did not match any configured fetch allowlist site.
	ErrURLNotAllowed = errors.New("url not in allowlist")
	// ErrFetchMethodNotAllowed reports that a URL matched a site, but the method is not allowed there.
	ErrFetchMethodNotAllowed = errors.New("method not allowed for site")
)

// FetchRequest identifies a URL to fetch.
type FetchRequest struct {
	URL     string
	Method  string
	Body    string
	Headers map[string]string
}

// FetchResponse holds the result of a whitelisted HTTP fetch.
type FetchResponse struct {
	StatusCode int
	Headers    map[string]string
	Body       string
	FromSite   string
	Truncated  bool
}

// URLFetcher performs HTTP requests against operator-approved sites.
type URLFetcher interface {
	Fetch(ctx context.Context, req FetchRequest) (FetchResponse, error)
}
