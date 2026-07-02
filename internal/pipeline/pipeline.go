// Package pipeline runs the idempotent ingest -> chunk -> embed -> store flow
// (plan §9). Postgres (via ent) is the source of truth; Qdrant holds vectors.
package pipeline

import (
	"context"
	"sync"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/go-faster/scpbot/internal/ent"
	"github.com/go-faster/scpbot/internal/ent/chunk"
	"github.com/go-faster/scpbot/internal/ent/document"
	"github.com/go-faster/scpbot/internal/index"
)

// VectorStore is the subset of the Qdrant store the pipeline needs.
type VectorStore interface {
	Upsert(ctx context.Context, chunks []index.Chunk, vectors [][]float32) error
	Delete(ctx context.Context, ids []uuid.UUID) error
}

// PipelineOptions configures the Pipeline.
type PipelineOptions struct {
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
}

func (opts *PipelineOptions) setDefaults() {
	if opts.TracerProvider == nil {
		opts.TracerProvider = otel.GetTracerProvider()
	}
	if opts.MeterProvider == nil {
		opts.MeterProvider = otel.GetMeterProvider()
	}
}

// Pipeline indexes Documents into Postgres + Qdrant.
type Pipeline struct {
	db       *ent.Client
	chunker  index.Chunker
	embedder index.Embedder
	vectors  VectorStore
	tracer   trace.Tracer
	metrics  *pipelineMetrics
	docLocks *keyLocker
}

// New builds a Pipeline. vectors may be nil to skip vector indexing.
func New(db *ent.Client, chunker index.Chunker, embedder index.Embedder, vectors VectorStore, opts PipelineOptions) (*Pipeline, error) {
	opts.setDefaults()
	m, err := newPipelineMetrics(opts.MeterProvider)
	if err != nil {
		return nil, errors.Wrap(err, "pipeline metrics")
	}
	return &Pipeline{
		db:       db,
		chunker:  chunker,
		embedder: embedder,
		vectors:  vectors,
		tracer:   opts.TracerProvider.Tracer("github.com/go-faster/scpbot/pipeline"),
		metrics:  m,
		docLocks: newKeyLocker(),
	}, nil
}

type keyLock struct {
	mu   sync.Mutex
	refs int
}

type keyLocker struct {
	mu    sync.Mutex
	locks map[string]*keyLock
}

func newKeyLocker() *keyLocker {
	return &keyLocker{locks: make(map[string]*keyLock)}
}

func (l *keyLocker) lock(key string) func() {
	l.mu.Lock()
	kl := l.locks[key]
	if kl == nil {
		kl = new(keyLock)
		l.locks[key] = kl
	}
	kl.refs++
	l.mu.Unlock()

	kl.mu.Lock()
	return func() {
		kl.mu.Unlock()

		l.mu.Lock()
		defer l.mu.Unlock()
		kl.refs--
		if kl.refs == 0 {
			delete(l.locks, key)
		}
	}
}

type chunkKey struct {
	index    int
	textHash string
}

// Index processes a single Document idempotently: it skips work when the body
// hash is unchanged, otherwise (re)chunks, embeds, and upserts (plan §9).
func (p *Pipeline) Index(ctx context.Context, doc index.Document) (rerr error) {
	if doc.BodyHash == "" {
		doc.BodyHash = index.Hash(doc.Body)
	}
	unlock := p.docLocks.lock(string(doc.Source) + "\x00" + doc.SourceID)
	defer unlock()

	ctx, span := p.tracer.Start(ctx, "pipeline.Index",
		trace.WithAttributes(
			attribute.String("source", string(doc.Source)),
			attribute.String("source_id", doc.SourceID),
		),
	)
	defer func() {
		if rerr != nil {
			span.RecordError(rerr)
			span.SetStatus(codes.Error, rerr.Error())
		}
		span.End()
	}()

	// Skip if an identical document (same source/source_id/body_hash) exists.
	exists, err := p.db.Document.Query().
		Where(
			document.Source(string(doc.Source)),
			document.SourceID(doc.SourceID),
			document.BodyHash(doc.BodyHash),
		).Exist(ctx)
	if err != nil {
		return errors.Wrap(err, "query document")
	}
	if exists {
		zctx.From(ctx).Debug("document unchanged, skipping",
			zap.String("source", string(doc.Source)), zap.String("source_id", doc.SourceID))
		p.metrics.documents.Add(ctx, 1,
			metric.WithAttributes(
				attribute.String("source", string(doc.Source)),
				attribute.String("status", "skipped"),
			),
		)
		return nil
	}

	chunks, err := p.chunker.Chunk(ctx, doc)
	if err != nil {
		return errors.Wrap(err, "chunk")
	}
	for i := range chunks {
		if chunks[i].TextHash == "" {
			chunks[i].TextHash = index.Hash(chunks[i].Text)
		}
	}

	// Load existing chunks for this document.
	existingChunks, err := p.db.Chunk.Query().
		Where(chunk.DocumentID(doc.ID)).
		All(ctx)
	if err != nil {
		return errors.Wrap(err, "query existing chunks")
	}

	// Build lookup by (chunk_index, text_hash) — chunkers produce fresh random
	// UUIDs each call, so the stable dedup key is the pair (index, text_hash).
	existingByKey := make(map[chunkKey]*ent.Chunk)
	for _, ec := range existingChunks {
		key := chunkKey{index: ec.ChunkIndex, textHash: ec.TextHash}
		existingByKey[key] = ec
	}

	var (
		toEmbed   []index.Chunk
		staleIDs  []uuid.UUID
		qdrantIDs = make(map[uuid.UUID]uuid.UUID) // chunk.ID → qdrant_point_id to preserve
	)

	newChunkKeys := make(map[chunkKey]bool)
	for i := range chunks {
		c := &chunks[i]
		key := chunkKey{index: c.Index, textHash: c.TextHash}
		newChunkKeys[key] = true

		ec, ok := existingByKey[key]
		if ok && ec.QdrantPointID != nil {
			// Already embedded — reuse existing IDs so qdrant_point_id
			// stays valid.
			c.ID = ec.ID
			qdrantIDs[ec.ID] = *ec.QdrantPointID
			continue
		}
		toEmbed = append(toEmbed, *c)
	}

	for _, ec := range existingChunks {
		key := chunkKey{index: ec.ChunkIndex, textHash: ec.TextHash}
		if !newChunkKeys[key] && ec.QdrantPointID != nil {
			staleIDs = append(staleIDs, ec.ID)
		}
	}

	// Embed before touching Postgres: if embedding or the vector upsert fails,
	// nothing is persisted, so a retry re-chunks and re-embeds instead of being
	// skipped by the "document exists" check above with un-embedded chunks stuck
	// forever.
	if p.vectors != nil && p.embedder != nil && len(toEmbed) > 0 {
		start := time.Now()
		if err := p.embed(ctx, toEmbed); err != nil {
			p.metrics.documents.Add(ctx, 1,
				metric.WithAttributes(
					attribute.String("source", string(doc.Source)),
					attribute.String("status", "error"),
				),
			)
			return errors.Wrap(err, "embed")
		}
		p.metrics.embedDur.Record(ctx, time.Since(start).Seconds())
		for i := range toEmbed {
			qdrantIDs[toEmbed[i].ID] = toEmbed[i].ID
		}
	}

	if err := p.persist(ctx, doc, chunks, qdrantIDs); err != nil {
		p.metrics.documents.Add(ctx, 1,
			metric.WithAttributes(
				attribute.String("source", string(doc.Source)),
				attribute.String("status", "error"),
			),
		)
		return errors.Wrap(err, "persist")
	}

	if p.vectors != nil && len(staleIDs) > 0 {
		if err := p.vectors.Delete(ctx, staleIDs); err != nil {
			zctx.From(ctx).Error("failed to delete stale vector points",
				zap.Error(err),
				zap.Int("count", len(staleIDs)),
			)
		}
	}

	p.metrics.documents.Add(ctx, 1,
		metric.WithAttributes(
			attribute.String("source", string(doc.Source)),
			attribute.String("status", "indexed"),
		),
	)
	p.metrics.chunks.Add(ctx, int64(len(toEmbed)),
		metric.WithAttributes(
			attribute.String("status", "embedded"),
		),
	)
	p.metrics.chunks.Add(ctx, int64(len(chunks)-len(toEmbed)),
		metric.WithAttributes(
			attribute.String("status", "reused"),
		),
	)

	zctx.From(ctx).Info("indexed document",
		zap.String("source", string(doc.Source)),
		zap.String("source_id", doc.SourceID),
		zap.Int("chunks", len(chunks)))
	return nil
}

func (p *Pipeline) persist(ctx context.Context, doc index.Document, chunks []index.Chunk, qdrantIDs map[uuid.UUID]uuid.UUID) error {
	return withTx(ctx, p.db, func(tx *ent.Tx) error {
		err := tx.Document.Create().
			SetID(doc.ID).
			SetSource(string(doc.Source)).
			SetSourceID(doc.SourceID).
			SetSourceURL(doc.URL).
			SetTitle(doc.Title).
			SetBody(doc.Body).
			SetBodyHash(doc.BodyHash).
			SetMetadata(doc.Metadata).
			SetCreatedAt(doc.CreatedAt).
			SetUpdatedAt(doc.UpdatedAt).
			OnConflictColumns("source", "source_id", "body_hash").
			UpdateNewValues().
			Exec(ctx)
		if err != nil {
			return errors.Wrap(err, "upsert document")
		}

		for i := range chunks {
			c := chunks[i]
			create := tx.Chunk.Create().
				SetID(c.ID).
				SetDocumentID(doc.ID).
				SetChunkIndex(c.Index).
				SetChunkType(string(c.Type)).
				SetTitle(c.Title).
				SetText(c.Text).
				SetTextHash(c.TextHash).
				SetMetadata(c.Metadata).
				SetTokenCount(c.TokenCount)
			if qpID, ok := qdrantIDs[c.ID]; ok {
				create = create.SetQdrantPointID(qpID)
			}
			err := create.
				OnConflictColumns("document_id", "chunk_index", "text_hash").
				UpdateNewValues().
				Exec(ctx)
			if err != nil {
				return errors.Wrapf(err, "upsert chunk %d", c.Index)
			}
		}
		return nil
	})
}

// embed produces vectors for chunks and upserts them into Qdrant. It does not
// touch Postgres — the caller records the resulting qdrant_point_id (== chunk
// ID) when it persists the chunks, so a failure here leaves no DB trace to
// clean up on retry.
func (p *Pipeline) embed(ctx context.Context, chunks []index.Chunk) error {
	texts := make([]string, len(chunks))
	for i := range chunks {
		texts[i] = chunks[i].Text
	}
	vectors, err := p.embedder.Embed(ctx, texts)
	if err != nil {
		return errors.Wrap(err, "embed texts")
	}
	if err := p.vectors.Upsert(ctx, chunks, vectors); err != nil {
		return errors.Wrap(err, "upsert vectors")
	}
	return nil
}

func withTx(ctx context.Context, db *ent.Client, fn func(tx *ent.Tx) error) error {
	tx, err := db.Tx(ctx)
	if err != nil {
		return errors.Wrap(err, "begin tx")
	}
	defer func() {
		if v := recover(); v != nil {
			_ = tx.Rollback()
			panic(v)
		}
	}()
	if err := fn(tx); err != nil {
		if rerr := tx.Rollback(); rerr != nil {
			return errors.Wrapf(err, "rollback: %v", rerr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return errors.Wrap(err, "commit")
	}
	return nil
}

var _ = sql.OrderDesc // keep entsql import available for future filters
