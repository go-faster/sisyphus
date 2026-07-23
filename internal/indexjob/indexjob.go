// Package indexjob carries walked documents from the process that fetches them
// to the process that indexes them.
//
// Ingestion has two halves with opposite scaling properties. Fetching is
// stateful and single-owner: it holds a git clone, a Telegram session, source
// credentials, and it advances a cursor that only one writer may move. Indexing
// — chunk, embed, upsert Postgres and Qdrant — is stateless and idempotent on
// (source, source_id), so it parallelises across as many replicas as there is
// embedding capacity for, and embedding is where the time actually goes.
//
// A job therefore carries the document itself rather than a reference to it.
// That is what lets a worker run with no source credentials, no clone and no
// session file: everything it needs is in the payload. The cost is that a
// document crosses a JSON boundary it never used to, which is not free — see
// [Decode].
package indexjob

import (
	"github.com/go-faster/errors"

	chunkcode "github.com/go-faster/sisyphus/internal/chunk/code"
	chunkgit "github.com/go-faster/sisyphus/internal/chunk/git"
	chunkgitlab "github.com/go-faster/sisyphus/internal/chunk/gitlab"
	chunkjira "github.com/go-faster/sisyphus/internal/chunk/jira"
	chunkmd "github.com/go-faster/sisyphus/internal/chunk/markdown"
	chunktg "github.com/go-faster/sisyphus/internal/chunk/telegram"
	chunkyaml "github.com/go-faster/sisyphus/internal/chunk/yaml"
	"github.com/go-faster/sisyphus/internal/index"
)

// QueueName is the queue name index jobs are published under.
const QueueName = "ingest.index"

// Kind names the chunker a document must be indexed with.
//
// It travels in the payload because the chunker is the producer's choice, not
// a property of the document: git's Markdown docs and a context-files source
// both produce index.SourceGitDocs-shaped documents chunked by the same
// Markdown chunker, while a repo's YAML manifests and its source files are the
// same walk split three ways. The worker cannot re-derive it from doc.Source.
type Kind string

// Kinds, one per chunker the ingestion runs use.
const (
	KindMarkdown Kind = "markdown"
	KindYAML     Kind = "yaml"
	KindCode     Kind = "code"
	KindGit      Kind = "git"
	KindGitLab   Kind = "gitlab"
	KindJira     Kind = "jira"
	KindTelegram Kind = "telegram"
)

// Chunker builds the chunker for k.
//
// Every kind is constructed here rather than by each caller so a producer's
// chunker-version filter and its worker's chunker cannot disagree. They
// disagreeing is not a loud failure: the producer would filter against one
// version and the worker index under another, so every document would look
// permanently changed and re-embed on every poll.
func Chunker(k Kind) (index.Chunker, error) {
	switch k {
	case KindMarkdown:
		return chunkmd.New(chunkmd.ChunkerOptions{}), nil
	case KindYAML:
		return chunkyaml.New(chunkyaml.ChunkerOptions{}), nil
	case KindCode:
		return chunkcode.New(chunkcode.ChunkerOptions{}), nil
	case KindGit:
		return chunkgit.New(), nil
	case KindGitLab:
		return chunkgitlab.New(), nil
	case KindJira:
		return chunkjira.New(), nil
	case KindTelegram:
		return chunktg.New(), nil
	default:
		return nil, errors.Errorf("unknown index job kind %q", k)
	}
}

// Kinds lists every kind, for a worker building one pipeline per kind up front.
func Kinds() []Kind {
	return []Kind{
		KindMarkdown,
		KindYAML,
		KindCode,
		KindGit,
		KindGitLab,
		KindJira,
		KindTelegram,
	}
}
