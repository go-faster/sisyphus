// Package pipeline runs the idempotent ingest -> chunk -> embed -> store flow
// (plan §9). Postgres (via ent) is the source of truth; Qdrant holds vectors.
package pipeline

import (
	"context"

	"entgo.io/ent/dialect/sql"
	"github.com/go-faster/errors"
	"go.uber.org/zap"

	"github.com/go-faster/scpbot/internal/ent"
	"github.com/go-faster/scpbot/internal/ent/chunk"
	"github.com/go-faster/scpbot/internal/ent/document"
	"github.com/go-faster/scpbot/internal/index"
)

// VectorStore is the subset of the Qdrant store the pipeline needs.
type VectorStore interface {
	Upsert(ctx context.Context, chunks []index.Chunk, vectors [][]float32) error
}

// Pipeline indexes Documents into Postgres + Qdrant.
type Pipeline struct {
	db       *ent.Client
	chunker  index.Chunker
	embedder index.Embedder
	vectors  VectorStore
	log      *zap.Logger
}

// New builds a Pipeline. vectors may be nil to skip vector indexing.
func New(db *ent.Client, chunker index.Chunker, embedder index.Embedder, vectors VectorStore, log *zap.Logger) *Pipeline {
	if log == nil {
		log = zap.NewNop()
	}
	return &Pipeline{db: db, chunker: chunker, embedder: embedder, vectors: vectors, log: log}
}

// Index processes a single Document idempotently: it skips work when the body
// hash is unchanged, otherwise (re)chunks, embeds, and upserts (plan §9).
func (p *Pipeline) Index(ctx context.Context, doc index.Document) error {
	if doc.BodyHash == "" {
		doc.BodyHash = index.Hash(doc.Body)
	}

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
		p.log.Debug("document unchanged, skipping",
			zap.String("source", string(doc.Source)), zap.String("source_id", doc.SourceID))
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

	if err := p.persist(ctx, doc, chunks); err != nil {
		return errors.Wrap(err, "persist")
	}

	if p.vectors != nil && p.embedder != nil && len(chunks) > 0 {
		if err := p.embed(ctx, chunks); err != nil {
			return errors.Wrap(err, "embed")
		}
	}
	p.log.Info("indexed document",
		zap.String("source", string(doc.Source)),
		zap.String("source_id", doc.SourceID),
		zap.Int("chunks", len(chunks)))
	return nil
}

func (p *Pipeline) persist(ctx context.Context, doc index.Document, chunks []index.Chunk) error {
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
			err := tx.Chunk.Create().
				SetID(c.ID).
				SetDocumentID(doc.ID).
				SetChunkIndex(c.Index).
				SetChunkType(string(c.Type)).
				SetTitle(c.Title).
				SetText(c.Text).
				SetTextHash(c.TextHash).
				SetMetadata(c.Metadata).
				SetTokenCount(c.TokenCount).
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
	// Record qdrant_point_id = chunk id so we can reconcile later.
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
