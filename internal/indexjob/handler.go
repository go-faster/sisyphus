package indexjob

import (
	"context"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/index"
	"github.com/go-faster/sisyphus/internal/pipeline"
	"github.com/go-faster/sisyphus/internal/queue"
)

// HandlerOptions configures [NewHandler].
type HandlerOptions struct {
	Pipeline pipeline.PipelineOptions
}

// Handler runs index jobs, one pipeline per [Kind].
type Handler struct {
	pipelines map[Kind]pipeline.Indexer
}

// NewHandler builds a pipeline for every kind up front, so an unknown kind is a
// startup error rather than a job that fails its whole attempt budget at
// runtime. vectors may be nil to skip vector indexing.
func NewHandler(db *ent.Client, embedder index.Embedder, vectors pipeline.VectorStore, opts HandlerOptions) (*Handler, error) {
	pipelines := make(map[Kind]pipeline.Indexer, len(Kinds()))
	for _, k := range Kinds() {
		chunker, err := Chunker(k)
		if err != nil {
			return nil, err
		}
		p, err := pipeline.New(db, chunker, embedder, vectors, opts.Pipeline)
		if err != nil {
			return nil, errors.Wrapf(err, "build %s pipeline", k)
		}
		pipelines[k] = p
	}
	return &Handler{pipelines: pipelines}, nil
}

// Handle indexes one job. It is idempotent: the queue is at-least-once, and
// pipeline.Index is a no-op for a document already current.
func (h *Handler) Handle(ctx context.Context, d queue.Delivery) error {
	p, err := Decode(d.Payload)
	if err != nil {
		// A payload this worker cannot parse will not parse on retry either.
		// Returning the error still burns the attempt budget rather than
		// discarding the job, so it lands in the queue's terminal status where
		// an operator can see it.
		return errors.Wrap(err, "decode index job")
	}
	pipe, ok := h.pipelines[p.Kind]
	if !ok {
		return errors.Errorf("unknown index job kind %q", p.Kind)
	}

	zctx.From(ctx).Debug("indexing queued document",
		zap.String("kind", string(p.Kind)),
		zap.String("source", string(p.Document.Source)),
		zap.String("source_id", p.Document.SourceID),
	)
	if err := pipe.Index(ctx, p.Document); err != nil {
		return errors.Wrapf(err, "index %s/%s", p.Document.Source, p.Document.SourceID)
	}
	return nil
}
