// Package vectorrepair rebinds chunks whose vector point is keyed by the wrong
// ID.
//
// A chunk's point must be keyed by the chunk's own ID: retrieval hydrates a
// vector hit's text from Postgres by chunk ID, so a point stored under any other
// ID resolves to empty text — the chunk stays searchable but contributes
// nothing, taking a result slot to say it.
//
// Rows drifted when a document was indexed while the vector store was down
// (leaving qdrant_point_id NULL) and later re-indexed: the unchanged chunks
// matched existing rows, but were embedded under the chunker's fresh UUID while
// the upsert kept the row's original ID. internal/pipeline no longer does that;
// this package repairs rows written before it stopped.
//
// Repair re-embeds the chunk's own text and rewrites the point under the chunk's
// ID. The old point is deleted afterwards, so an interrupted run leaves an
// orphan for `ssingest gc` rather than a chunk pointing at nothing.
package vectorrepair

import (
	"context"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/ent/chunk"
	"github.com/go-faster/sisyphus/internal/index"
)

// VectorStore is the subset of the vector store the repairer needs.
type VectorStore interface {
	Upsert(ctx context.Context, chunks []index.Chunk, vectors [][]float32) error
	Delete(ctx context.Context, ids []uuid.UUID) error
}

// Options configures a Repairer.
type Options struct {
	// Batch is how many chunks to re-embed at a time.
	Batch int
	// DryRun reports what would be repaired without writing.
	DryRun bool
	// Logger receives progress. Defaults to the context logger.
	Logger *zap.Logger
}

const defaultBatch = 64

func (opts *Options) setDefaults(ctx context.Context) {
	if opts.Batch <= 0 {
		opts.Batch = defaultBatch
	}
	if opts.Logger == nil {
		opts.Logger = zctx.From(ctx)
	}
}

// Report summarizes a repair run.
type Report struct {
	// Mismatched is how many chunks were found bound to the wrong point.
	Mismatched int
	// Repaired is how many were rebound (0 when DryRun).
	Repaired int
	// DryRun echoes whether writing was suppressed.
	DryRun bool
}

// Repairer rebinds mismatched chunks to points keyed by their own ID.
type Repairer struct {
	db       *ent.Client
	embedder index.Embedder
	vectors  VectorStore
	opts     Options
}

// New builds a Repairer. All dependencies are required.
func New(db *ent.Client, embedder index.Embedder, vectors VectorStore, opts Options) (*Repairer, error) {
	if db == nil {
		return nil, errors.New("vectorrepair: db required")
	}
	if embedder == nil {
		return nil, errors.New("vectorrepair: embedder required")
	}
	if vectors == nil {
		return nil, errors.New("vectorrepair: vector store required")
	}
	return &Repairer{db: db, embedder: embedder, vectors: vectors, opts: opts}, nil
}

// mismatched matches chunks bound to a point that is not their own ID.
func mismatched() func(*entsql.Selector) {
	return func(s *entsql.Selector) {
		s.Where(entsql.And(
			entsql.NotNull(s.C(chunk.FieldQdrantPointID)),
			entsql.ColumnsNEQ(s.C(chunk.FieldID), s.C(chunk.FieldQdrantPointID)),
		))
	}
}

// Count reports how many chunks are bound to the wrong point.
func (r *Repairer) Count(ctx context.Context) (int, error) {
	n, err := r.db.Chunk.Query().Where(mismatched()).Count(ctx)
	if err != nil {
		return 0, errors.Wrap(err, "count mismatched chunks")
	}
	return n, nil
}

// Run repairs every mismatched chunk.
//
// Each batch is re-embedded and written under the chunk's own ID before the row
// is updated and the old point deleted, so the row never points at something
// that does not exist. Repairing a batch removes it from the query, so the loop
// drains naturally.
func (r *Repairer) Run(ctx context.Context) (Report, error) {
	r.opts.setDefaults(ctx)
	lg := r.opts.Logger

	total, err := r.Count(ctx)
	if err != nil {
		return Report{}, err
	}
	rep := Report{Mismatched: total, DryRun: r.opts.DryRun}
	lg.Info("vector repair scan complete", zap.Int("mismatched", total))
	if total == 0 || r.opts.DryRun {
		return rep, nil
	}

	for {
		rows, err := r.db.Chunk.Query().
			Where(mismatched()).
			Limit(r.opts.Batch).
			All(ctx)
		if err != nil {
			return rep, errors.Wrap(err, "query mismatched chunks")
		}
		if len(rows) == 0 {
			break
		}
		n, err := r.repairBatch(ctx, rows)
		rep.Repaired += n
		if err != nil {
			return rep, err
		}
		if n == 0 {
			// Nothing moved: stop rather than spin on the same rows forever.
			return rep, errors.New("vectorrepair: batch made no progress")
		}
		lg.Info("vector repair progress",
			zap.Int("repaired", rep.Repaired),
			zap.Int("total", total),
		)
	}

	lg.Info("vector repair complete", zap.Int("repaired", rep.Repaired))
	return rep, nil
}

// repairBatch rebinds one batch, returning how many rows were rebound.
func (r *Repairer) repairBatch(ctx context.Context, rows []*ent.Chunk) (int, error) {
	texts := make([]string, len(rows))
	chunks := make([]index.Chunk, len(rows))
	oldPoints := make([]uuid.UUID, 0, len(rows))
	for i, row := range rows {
		texts[i] = row.Text
		chunks[i] = index.Chunk{
			ID:         row.ID, // the point must be keyed by the chunk's own ID
			DocumentID: row.DocumentID,
			Index:      row.ChunkIndex,
			Type:       index.ChunkType(row.ChunkType),
			Title:      row.Title,
			Text:       row.Text,
			TextHash:   row.TextHash,
			TokenCount: row.TokenCount,
			Metadata:   row.Metadata,
		}
		if row.QdrantPointID != nil {
			oldPoints = append(oldPoints, *row.QdrantPointID)
		}
	}

	vectors, err := r.embedder.Embed(ctx, texts)
	if err != nil {
		return 0, errors.Wrap(err, "embed chunks for repair")
	}
	if len(vectors) != len(chunks) {
		return 0, errors.Errorf("embedder returned %d vectors for %d chunks", len(vectors), len(chunks))
	}

	// Write the correct point first: until the row is updated this is an
	// unreferenced point, which gc reclaims. The reverse order would leave the
	// row pointing at something that does not exist yet.
	if err := r.vectors.Upsert(ctx, chunks, vectors); err != nil {
		return 0, errors.Wrap(err, "upsert repaired points")
	}

	var repaired int
	for _, row := range rows {
		if err := r.db.Chunk.UpdateOneID(row.ID).SetQdrantPointID(row.ID).Exec(ctx); err != nil {
			return repaired, errors.Wrap(err, "rebind chunk to its own point")
		}
		repaired++
	}

	// Only now is the old point unreferenced and safe to drop.
	if len(oldPoints) > 0 {
		if err := r.vectors.Delete(ctx, oldPoints); err != nil {
			// Non-fatal: the rows are correct now, and the strays are exactly
			// what gc exists to reclaim.
			r.opts.Logger.Warn("could not delete superseded points, leaving them for gc",
				zap.Error(err),
				zap.Int("count", len(oldPoints)),
			)
		}
	}
	return repaired, nil
}
