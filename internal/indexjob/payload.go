package indexjob

import (
	"encoding/json"

	"github.com/go-faster/errors"

	chunkgitlab "github.com/go-faster/sisyphus/internal/chunk/gitlab"
	chunkjira "github.com/go-faster/sisyphus/internal/chunk/jira"
	"github.com/go-faster/sisyphus/internal/index"
)

// Payload is the wire form of one index job: a single document plus the
// chunker to index it with.
//
// One document per job, not a batch. The producer only enqueues documents that
// would actually be re-indexed (see [Publisher]), so job count tracks change
// rather than corpus size, and per-document jobs keep the retry unit equal to
// the failure unit — one document failing to embed does not drag a batch of
// healthy ones through the same retries.
type Payload struct {
	// Version is the shape this payload was written in; see [Version].
	Version  int            `json:"version"`
	Kind     Kind           `json:"kind"`
	Document index.Document `json:"document"`
}

// Version is the current index-job payload shape.
//
// A queued job outlives the binary that wrote it: a worker rolled forward
// while jobs are outstanding drains payloads the previous release produced. As
// long as the change is additive that is fine, but the document's metadata
// carries source structs (`gitlab_mr`, `jira_issue` — see [Decode]), and a
// field of one changing Go type is not additive. Without a version the worker
// either fails on a type error that names no cause, or — worse, where the JSON
// still fits — decodes the old shape into the new struct and indexes a wrong
// answer silently.
//
// So: bump this whenever a rehydrated metadata struct changes shape
// incompatibly. Version 0 is the unversioned payload that predates this field,
// and is rejected outright — an unversioned job is exactly the one whose shape
// nothing vouches for.
//
// Version 1 is the first versioned shape, and the first that is guaranteed to
// carry a GitLab MR whose Author and MergedBy are objects rather than the bare
// strings they were until they had to address a notification.
const Version = 1

// Encode marshals a job payload in the current [Version].
func Encode(kind Kind, doc index.Document) ([]byte, error) {
	b, err := json.Marshal(Payload{Version: Version, Kind: kind, Document: doc})
	if err != nil {
		return nil, errors.Wrap(err, "marshal index job")
	}
	return b, nil
}

// ErrVersion reports an index job written in a shape this build does not
// understand. The worker's attempt budget carries such a job into the queue's
// terminal status rather than retrying it forever: no number of retries makes
// an old payload readable, and the document is re-enqueued by the next run
// that sees its content change, or by `ssingest <source> --reset`.
var ErrVersion = errors.New("unsupported index job payload version")

// Canonicalize returns doc in the shape it takes after crossing the queue.
//
// It exists so indexing in-process and indexing from the queue cannot produce
// different rows for the same document. JSON does not preserve a metadata
// value's Go type — index.Document.Metadata is map[string]any — and that type
// is observable downstream: internal/search/qdrant maps an int to a Qdrant
// integer and a float64 to a double, and drops a []string entirely because
// qdrant.NewValue has no case for it (a []any of the same strings converts
// fine). So the same document indexed by `ssingest gitlab` and by `ssingest
// serve` would otherwise land in Qdrant with different payload types.
//
// Normalizing both paths through here settles that on the queue's shape. It is
// inert for retrieval today: every Qdrant condition is a keyword match (see
// buildFilter), so nothing filters on a numeric or list-valued metadata key.
//
// Numbers survive as float64, which is exact below 2^53 — every numeric id
// this handles (GitLab IIDs, Telegram chat and message ids) is far under that.
func Canonicalize(doc index.Document) (index.Document, error) {
	b, err := json.Marshal(doc)
	if err != nil {
		return index.Document{}, errors.Wrap(err, "marshal document")
	}
	var out index.Document
	if err := json.Unmarshal(b, &out); err != nil {
		return index.Document{}, errors.Wrap(err, "unmarshal document")
	}
	if err := rehydrate(out.Metadata); err != nil {
		return index.Document{}, errors.Wrap(err, "rehydrate document metadata")
	}
	return out, nil
}

// Decode unmarshals a job payload and restores the metadata values JSON cannot
// round-trip on its own.
//
// The GitLab and Jira ingesters stash a concrete Go struct in the document's
// metadata map — index.Document.Metadata is map[string]any — and their chunkers
// recover it by type assertion:
//
//	if iss, ok := doc.Metadata["gitlab_issue"].(Issue); ok { ... }
//
// json.Unmarshal turns that struct into a map[string]any, so the assertion
// fails and the chunker falls through to its fallback path: one flat chunk
// instead of typed summary/comment chunks, with no error and no log line. Every
// issue and MR would quietly index worse than before, and only a full --reset
// would ever fix it. Rehydrating here is what keeps the queue boundary
// invisible to the chunkers.
//
// A new struct-valued metadata key needs a case added below. TestRoundTripChunks
// is what catches one that was not.
func Decode(b []byte) (Payload, error) {
	var p Payload
	if err := json.Unmarshal(b, &p); err != nil {
		return Payload{}, errors.Wrap(err, "unmarshal index job")
	}
	// Checked before rehydrating, not after: rehydration is where an old shape
	// would be decoded into today's structs, so the version has to be the
	// thing that stops it.
	if p.Version != Version {
		return Payload{}, errors.Wrapf(ErrVersion, "have %d, want %d", p.Version, Version)
	}
	if err := rehydrate(p.Document.Metadata); err != nil {
		return Payload{}, errors.Wrap(err, "rehydrate document metadata")
	}
	return p, nil
}

func rehydrate(md map[string]any) error {
	if md == nil {
		return nil
	}
	if err := rehydrateKey[chunkgitlab.Issue](md, "gitlab_issue"); err != nil {
		return err
	}
	if err := rehydrateKey[chunkgitlab.MergeRequest](md, "gitlab_mr"); err != nil {
		return err
	}
	if err := rehydrateKey[chunkgitlab.Release](md, "gitlab_release"); err != nil {
		return err
	}
	return rehydrateKey[chunkjira.Issue](md, "jira_issue")
}

// rehydrateKey re-decodes md[key] into T. It is a no-op when the key is absent
// or already holds a T, so decoding a payload twice, or a document that never
// crossed the wire, is harmless.
func rehydrateKey[T any](md map[string]any, key string) error {
	raw, ok := md[key]
	if !ok || raw == nil {
		return nil
	}
	if _, done := raw.(T); done {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return errors.Wrapf(err, "re-marshal %s", key)
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return errors.Wrapf(err, "decode %s", key)
	}
	md[key] = v
	return nil
}
