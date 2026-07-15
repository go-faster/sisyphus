// Package pipeline runs the idempotent ingest -> chunk -> embed -> store flow
// (plan §9). Postgres (via ent) is the source of truth; Qdrant holds vectors.
package pipeline

import (
	"context"
	"strings"
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

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/ent/chunk"
	"github.com/go-faster/sisyphus/internal/ent/document"
	"github.com/go-faster/sisyphus/internal/index"
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
		tracer:   opts.TracerProvider.Tracer("github.com/go-faster/sisyphus/internal/pipeline"),
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
	lockStart := time.Now()
	unlock := p.docLocks.lock(string(doc.Source) + "\x00" + doc.SourceID)
	defer unlock()
	p.metrics.lockWait.Record(ctx, time.Since(lockStart).Seconds(),
		metric.WithAttributes(attribute.String("source", string(doc.Source))),
	)

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

	// Look up existing document by identity (source, source_id).
	existing, err := p.db.Document.Query().
		Where(
			document.Source(string(doc.Source)),
			document.SourceID(doc.SourceID),
		).
		Only(ctx)
	switch {
	case ent.IsNotFound(err):
		// no existing document for this identity; doc.ID (set by the caller) is used as-is for a fresh insert
	case err != nil:
		return errors.Wrap(err, "query document")
	default:
		doc.ID = existing.ID // reuse the existing row's identity so chunk queries below operate on the right document_id
		if existing.BodyHash == doc.BodyHash {
			// unchanged — keep the existing skip-fast-path behavior
			span.SetAttributes(attribute.Bool("document.unchanged", true))
			span.AddEvent("document.unchanged")
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
	}

	phaseStart := time.Now()
	chunkCtx, chunkSpan := p.tracer.Start(ctx, "pipeline.chunk",
		trace.WithAttributes(attribute.String("source", string(doc.Source))),
	)
	chunks, err := p.chunker.Chunk(chunkCtx, doc)
	p.metrics.recordPhase(ctx, string(doc.Source), "chunk", time.Since(phaseStart).Seconds(), err)
	if err != nil {
		chunkSpan.RecordError(err)
		chunkSpan.SetStatus(codes.Error, err.Error())
		chunkSpan.End()
		return errors.Wrap(err, "chunk")
	}
	chunkSpan.SetAttributes(attribute.Int("chunks.total", len(chunks)))
	chunkSpan.AddEvent("chunk.done", trace.WithAttributes(attribute.Int("chunks.total", len(chunks))))
	chunkSpan.End()
	for i := range chunks {
		if chunks[i].TextHash == "" {
			chunks[i].TextHash = index.Hash(chunks[i].Text)
		}
		// Every chunk must carry its document's source in metadata: source-tier
		// and source-prefix filtering (both the Postgres FTS WHERE clause and the
		// Qdrant payload filter) key on metadata.source, and the default "curated"
		// tier lists jira/gitlab. Git/files/telegram chunkers set it, but the
		// jira/gitlab chunkers copy only the document metadata — which itself
		// omits source — so their chunks were silently unreachable through normal
		// search. Inject it centrally so no chunker can regress this.
		if chunks[i].Metadata == nil {
			chunks[i].Metadata = make(map[string]any, 2)
		}
		if _, ok := chunks[i].Metadata["source"]; !ok {
			chunks[i].Metadata["source"] = string(doc.Source)
		}
		// Likewise propagate the document's canonical URL as source_url so search
		// results and answers can render a "Source" link button. Only when absent:
		// git/files chunkers set a richer per-chunk source_url with line anchors
		// (#L10-20), which must win over the document-level URL. jira/gitlab set
		// neither on the chunk, so they inherit doc.URL here.
		if doc.URL != "" {
			if _, ok := chunks[i].Metadata["source_url"]; !ok {
				chunks[i].Metadata["source_url"] = doc.URL
			}
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
	span.SetAttributes(
		attribute.Bool("document.unchanged", false),
		attribute.Int("chunks.total", len(chunks)),
		attribute.Int("chunks.to_embed", len(toEmbed)),
		attribute.Int("chunks.reused", len(chunks)-len(toEmbed)),
		attribute.Int("chunks.stale", len(staleIDs)),
	)
	span.AddEvent("chunks.planned", trace.WithAttributes(
		attribute.Int("chunks.total", len(chunks)),
		attribute.Int("chunks.to_embed", len(toEmbed)),
		attribute.Int("chunks.reused", len(chunks)-len(toEmbed)),
		attribute.Int("chunks.stale", len(staleIDs)),
	))

	// Embed before touching Postgres: if embedding or the vector upsert fails,
	// nothing is persisted, so a retry re-chunks and re-embeds instead of being
	// skipped by the "document exists" check above with un-embedded chunks stuck
	// forever.
	if p.vectors != nil && p.embedder != nil && len(toEmbed) > 0 {
		start := time.Now()
		if err := p.embed(ctx, string(doc.Source), toEmbed); err != nil {
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

	phaseStart = time.Now()
	persistCtx, persistSpan := p.tracer.Start(ctx, "pipeline.persist",
		trace.WithAttributes(
			attribute.String("source", string(doc.Source)),
			attribute.Int("chunks.total", len(chunks)),
		),
	)
	if err := p.persist(persistCtx, doc, chunks, qdrantIDs, staleIDs); err != nil {
		p.metrics.recordPhase(ctx, string(doc.Source), "persist", time.Since(phaseStart).Seconds(), err)
		persistSpan.RecordError(err)
		persistSpan.SetStatus(codes.Error, err.Error())
		persistSpan.End()
		p.metrics.documents.Add(ctx, 1,
			metric.WithAttributes(
				attribute.String("source", string(doc.Source)),
				attribute.String("status", "error"),
			),
		)
		return errors.Wrap(err, "persist")
	}
	p.metrics.recordPhase(ctx, string(doc.Source), "persist", time.Since(phaseStart).Seconds(), nil)
	persistSpan.AddEvent("persist.done")
	persistSpan.End()

	if p.vectors != nil && len(staleIDs) > 0 {
		phaseStart = time.Now()
		deleteCtx, deleteSpan := p.tracer.Start(ctx, "pipeline.vector_delete",
			trace.WithAttributes(
				attribute.String("source", string(doc.Source)),
				attribute.Int("chunks.stale", len(staleIDs)),
			),
		)
		if err := p.vectors.Delete(deleteCtx, staleIDs); err != nil {
			p.metrics.recordPhase(ctx, string(doc.Source), "vector_delete", time.Since(phaseStart).Seconds(), err)
			deleteSpan.RecordError(err)
			deleteSpan.SetStatus(codes.Error, err.Error())
			deleteSpan.End()
			zctx.From(ctx).Error("failed to delete stale vector points",
				zap.Error(err),
				zap.Int("count", len(staleIDs)),
			)
		} else {
			p.metrics.recordPhase(ctx, string(doc.Source), "vector_delete", time.Since(phaseStart).Seconds(), nil)
			deleteSpan.AddEvent("vector_delete.done")
			deleteSpan.End()
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
		zap.Int("chunks", len(chunks)),
		zap.Int("embedded_chunks", len(toEmbed)),
		zap.Int("reused_chunks", len(chunks)-len(toEmbed)),
		zap.Int("stale_chunks", len(staleIDs)))
	return nil
}

func (p *Pipeline) persist(ctx context.Context, doc index.Document, chunks []index.Chunk, qdrantIDs map[uuid.UUID]uuid.UUID, staleIDs []uuid.UUID) error {
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
			OnConflictColumns("source", "source_id").
			UpdateNewValues().
			Exec(ctx)
		if err != nil {
			return errors.Wrap(err, "upsert document")
		}

		// Re-resolve the document's actual ID within this transaction: doc.ID
		// may be stale if a concurrent writer (e.g. an overlapping ssingest
		// run — docLocks only guards against races within this process)
		// created the row for this (source, source_id) after the pre-tx
		// lookup in Index but before this upsert ran. UpdateNewValues()
		// excludes the id column, so on conflict the existing row keeps its
		// original id; inserting chunks against the stale doc.ID would
		// violate the chunks->documents FK.
		persisted, err := tx.Document.Query().
			Where(
				document.Source(string(doc.Source)),
				document.SourceID(doc.SourceID),
			).
			Only(ctx)
		if err != nil {
			return errors.Wrap(err, "query persisted document")
		}
		docID := persisted.ID

		for i := range chunks {
			c := chunks[i]
			create := tx.Chunk.Create().
				SetID(c.ID).
				SetDocumentID(docID).
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

		// Delete stale chunks (no longer present in the new chunk set)
		if len(staleIDs) > 0 {
			if _, err := tx.Chunk.Delete().Where(chunk.IDIn(staleIDs...)).Exec(ctx); err != nil {
				return errors.Wrap(err, "delete stale chunks")
			}
		}

		return nil
	})
}

// embed produces vectors for chunks and upserts them into Qdrant. It does not
// touch Postgres — the caller records the resulting qdrant_point_id (== chunk
// ID) when it persists the chunks, so a failure here leaves no DB trace to
// clean up on retry.
func (p *Pipeline) embed(ctx context.Context, source string, chunks []index.Chunk) error {
	texts := make([]string, len(chunks))
	for i := range chunks {
		texts[i] = chunks[i].Text
	}
	start := time.Now()
	embedCtx, embedSpan := p.tracer.Start(ctx, "pipeline.embed",
		trace.WithAttributes(
			attribute.String("source", source),
			attribute.Int("chunks.count", len(chunks)),
		),
	)
	vectors, err := p.embedder.Embed(embedCtx, texts)
	if err != nil && shouldRetryEmbeddingsIndividually(err, chunks) {
		vectors, err = p.embedIndividually(embedCtx, chunks)
	}
	p.metrics.recordPhase(ctx, source, "embed", time.Since(start).Seconds(), err)
	if err != nil {
		embedSpan.RecordError(err)
		embedSpan.SetStatus(codes.Error, err.Error())
		embedSpan.End()
		return errors.Wrap(err, "embed texts")
	}
	embedSpan.AddEvent("embed.done", trace.WithAttributes(attribute.Int("vectors.count", len(vectors))))
	embedSpan.End()

	start = time.Now()
	upsertCtx, upsertSpan := p.tracer.Start(ctx, "pipeline.vector_upsert",
		trace.WithAttributes(
			attribute.String("source", source),
			attribute.Int("chunks.count", len(chunks)),
		),
	)
	if err := p.vectors.Upsert(upsertCtx, chunks, vectors); err != nil {
		p.metrics.recordPhase(ctx, source, "vector_upsert", time.Since(start).Seconds(), err)
		upsertSpan.RecordError(err)
		upsertSpan.SetStatus(codes.Error, err.Error())
		upsertSpan.End()
		return errors.Wrap(err, "upsert vectors")
	}
	p.metrics.recordPhase(ctx, source, "vector_upsert", time.Since(start).Seconds(), nil)
	upsertSpan.AddEvent("vector_upsert.done")
	upsertSpan.End()
	return nil
}

func shouldRetryEmbeddingsIndividually(err error, chunks []index.Chunk) bool {
	if len(chunks) < 2 {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "unsupported value: NaN") ||
		strings.Contains(msg, "unsupported value: +Inf") ||
		strings.Contains(msg, "unsupported value: -Inf")
}

func (p *Pipeline) embedIndividually(ctx context.Context, chunks []index.Chunk) ([][]float32, error) {
	vectors := make([][]float32, len(chunks))
	for i, c := range chunks {
		vecs, err := p.embedder.Embed(ctx, []string{c.Text})
		if err != nil {
			return nil, errors.Wrapf(err,
				"embed chunk index=%d id=%s title=%q text_hash=%s text_len=%d",
				c.Index, c.ID, c.Title, c.TextHash, len(c.Text),
			)
		}
		if len(vecs) != 1 {
			return nil, errors.Errorf("embed chunk index=%d id=%s returned %d vectors", c.Index, c.ID, len(vecs))
		}
		vectors[i] = vecs[0]
	}
	return vectors, nil
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
