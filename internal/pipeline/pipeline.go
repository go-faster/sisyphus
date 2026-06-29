// Package pipeline runs the idempotent ingest -> chunk -> embed -> store flow
// (plan §9). Postgres (via ent) is the source of truth; Qdrant holds vectors.
package pipeline

import (
	"context"
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
}

// New builds a Pipeline. vectors may be nil to skip vector indexing.
func New(db *ent.Client, chunker index.Chunker, embedder index.Embedder, vectors VectorStore, opts PipelineOptions) *Pipeline {
	opts.setDefaults()
	m, err := newPipelineMetrics(opts.MeterProvider)
	if err != nil {
		panic(err)
	}
	return &Pipeline{
		db:       db,
		chunker:  chunker,
		embedder: embedder,
		vectors:  vectors,
		tracer:   opts.TracerProvider.Tracer("github.com/go-faster/scpbot/pipeline"),
		metrics:  m,
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

	if err := p.persist(ctx, doc, chunks, qdrantIDs); err != nil {
		p.metrics.documents.Add(ctx, 1,
			metric.WithAttributes(
				attribute.String("source", string(doc.Source)),
				attribute.String("status", "error"),
			),
		)
		return errors.Wrap(err, "persist")
	}

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
	for i := range chunks {
		if err := p.db.Chunk.Update().
			Where(chunk.ID(chunks[i].ID)).
			SetQdrantPointID(chunks[i].ID).
			Exec(ctx); err != nil {
			return errors.Wrap(err, "set qdrant point id")
		}
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
