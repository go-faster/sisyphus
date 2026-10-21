package reconcile

import (
	"context"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/ent/chunk"
	"github.com/go-faster/sisyphus/internal/ent/document"
	"github.com/go-faster/sisyphus/internal/ent/predicate"
	"github.com/go-faster/sisyphus/internal/index"
)

// VectorStore is the subset of the vector store a delete needs.
type VectorStore interface {
	Delete(ctx context.Context, ids []uuid.UUID) error
}

// EntStore implements [Store] over ent.
type EntStore struct {
	db      *ent.Client
	vectors VectorStore
}

// NewEntStore builds a Store over db. vectors may be nil, in which case points
// are left for `ssingest gc` to reclaim — it exists for exactly this.
func NewEntStore(db *ent.Client, vectors VectorStore) *EntStore {
	return &EntStore{db: db, vectors: vectors}
}

var _ Store = (*EntStore)(nil)

// IndexedSourceIDs returns the source_ids indexed under source whose id starts
// with prefix. An empty prefix means the whole source.
func (s *EntStore) IndexedSourceIDs(ctx context.Context, source index.Source, prefix string) ([]string, error) {
	q := s.db.Document.Query().Where(document.Source(string(source)))
	if prefix != "" {
		q = q.Where(document.SourceIDHasPrefix(prefix))
	}

	var ids []string
	if err := q.Select(document.FieldSourceID).Scan(ctx, &ids); err != nil {
		return nil, errors.Wrap(err, "scan indexed source ids")
	}
	return ids, nil
}

// DeleteDocuments removes documents and their chunks in one transaction, then
// drops the chunks' vector points.
//
// Same order as the ingest-side prune: chunk IDs are captured before the delete
// (afterwards there is nothing to read them from), Postgres commits first, and
// the vector delete follows. A failure after the commit leaks points rather
// than orphaning rows, and leaked points are what gc collects — while a row
// pointing at a deleted point is unrecoverable.
func (s *EntStore) DeleteDocuments(ctx context.Context, source index.Source, sourceIDs []string) (int, error) {
	if len(sourceIDs) == 0 {
		return 0, nil
	}
	lg := zctx.From(ctx)

	// One match, used by all three statements, so the query that decides which
	// vector points to drop cannot drift from the deletes that orphan them.
	match := []predicate.Document{
		document.Source(string(source)),
		document.SourceIDIn(sourceIDs...),
	}

	chunkIDs, err := s.db.Chunk.Query().
		Where(chunk.HasDocumentWith(match...)).
		IDs(ctx)
	if err != nil {
		return 0, errors.Wrap(err, "query chunks to delete")
	}

	tx, err := s.db.Tx(ctx)
	if err != nil {
		return 0, errors.Wrap(err, "begin delete tx")
	}
	if _, err := tx.Chunk.Delete().
		Where(chunk.HasDocumentWith(match...)).
		Exec(ctx); err != nil {
		_ = tx.Rollback()
		return 0, errors.Wrap(err, "delete chunks")
	}
	if _, err := tx.Document.Delete().
		Where(match...).
		Exec(ctx); err != nil {
		_ = tx.Rollback()
		return 0, errors.Wrap(err, "delete documents")
	}
	if err := tx.Commit(); err != nil {
		return 0, errors.Wrap(err, "commit delete")
	}

	if s.vectors != nil && len(chunkIDs) > 0 {
		const batch = 1000
		for i := 0; i < len(chunkIDs); i += batch {
			j := min(i+batch, len(chunkIDs))
			if err := s.vectors.Delete(ctx, chunkIDs[i:j]); err != nil {
				// Non-fatal: the rows are already gone, so these points are
				// now unreferenced, which is precisely what gc sweeps.
				lg.Warn("vector delete for reconciled chunks (non-fatal, gc will reclaim)",
					zap.Error(err),
					zap.Int("count", j-i),
					zap.String("source", string(source)))
			}
		}
	}
	return len(chunkIDs), nil
}
