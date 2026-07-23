package indexjob

import (
	"context"

	"github.com/go-faster/errors"
	"github.com/google/uuid"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/index"
	"github.com/go-faster/sisyphus/internal/pipeline"
	"github.com/go-faster/sisyphus/internal/queue"
)

// PublisherOptions configures a [Publisher].
type PublisherOptions struct {
	// MaxAttempts overrides the queue's default attempt budget per document.
	MaxAttempts int
}

// Publisher hands a walked document to a worker instead of indexing it here.
//
// It satisfies the same Index(ctx, doc) shape as *pipeline.Pipeline, which is
// what lets an ingestion run be switched between indexing in-process and
// enqueuing without the run itself knowing which it is doing.
type Publisher struct {
	q           queue.Queue
	kind        Kind
	skipper     *pipeline.Skipper
	maxAttempts int
}

// NewPublisher builds a publisher for documents of kind.
func NewPublisher(q queue.Queue, kind Kind, db *ent.Client, opts PublisherOptions) (*Publisher, error) {
	chunker, err := Chunker(kind)
	if err != nil {
		return nil, err
	}
	return &Publisher{
		q:           q,
		kind:        kind,
		skipper:     pipeline.NewSkipper(db, chunker),
		maxAttempts: opts.MaxAttempts,
	}, nil
}

// Index enqueues doc for a worker, unless indexing it would be a no-op.
//
// The skip check is the reason this is affordable at all. A source walk re-reads
// everything every poll tick and almost none of it has changed; enqueuing it
// unfiltered would put the whole corpus on the queue every interval, and
// queue_jobs rows are currently never reclaimed (see the retention note in
// CLAUDE.md). Filtering here makes the queue carry change, not corpus.
//
// It costs one indexed lookup per document — exactly the lookup
// [pipeline.Pipeline.Index] does as its own first step, so the walk is no more
// expensive than it was when it indexed inline.
func (p *Publisher) Index(ctx context.Context, doc index.Document) error {
	if doc.BodyHash == "" {
		doc.BodyHash = index.Hash(doc.Body)
	}
	unchanged, err := p.skipper.Unchanged(ctx, doc)
	if err != nil {
		return errors.Wrap(err, "check document")
	}
	if unchanged {
		return nil
	}

	payload, err := Encode(p.kind, doc)
	if err != nil {
		return err
	}

	// A fresh key per publish, so the queue does no deduplication here.
	//
	// The tempting key is (source, source_id, body_hash), but the queue's dedup
	// covers a job's whole lifetime, not just while it is outstanding. A
	// document edited from A to B and reverted to A would find its original key
	// already used and be refused forever — the revert would never be indexed,
	// and nothing would report it. Redundant jobs are the cheaper mistake:
	// pipeline.Index skips an already-current document, so a duplicate costs one
	// query.
	id := uuid.New()
	if _, err := p.q.Publish(ctx, queue.Message{
		ID:          id,
		Key:         id.String(),
		Payload:     payload,
		MaxAttempts: p.maxAttempts,
	}); err != nil {
		return errors.Wrap(err, "publish index job")
	}
	return nil
}
