// Package index defines the shared contract for ingestion, chunking, embedding
// and search. It is intentionally dependency-light (stdlib + google/uuid) so
// every other package can depend on it without import cycles.
package index

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	SourceGitDocsPrefix    = "git_docs:"
	SourceGitCommitsPrefix = "git_commits:"
	SourceGitTagsPrefix    = "git_tags:"
)

// SourceGitDocs returns the Source for git-walked content of the given repo.
func SourceGitDocs(repo string) Source { return Source(SourceGitDocsPrefix + repo) }

// SourceGitCommit returns the Source for commit messages of the given repo.
func SourceGitCommit(repo string) Source { return Source(SourceGitCommitsPrefix + repo) }

// SourceGitTag returns the Source for git tags of the given repo.
func SourceGitTag(repo string) Source { return Source(SourceGitTagsPrefix + repo) }

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
	Text    string
	Service string            // optional service filter/boost
	Filters map[string]string // optional payload filters (source, repo, jira_key, ...)
	Limit   int
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

// Answerer constructs a final answer from retrieved context (plan §14). Deferred,
// stubbed for now.
type Answerer interface {
	Answer(ctx context.Context, question string, results []Result) (string, error)
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
