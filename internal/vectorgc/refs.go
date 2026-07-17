package vectorgc

import (
	"context"

	"github.com/go-faster/errors"
	"github.com/google/uuid"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/ent/chunk"
)

// EntRefStore answers "does a chunk still point at this?" from Postgres, the
// source of truth for which vector points are live.
type EntRefStore struct {
	db *ent.Client
}

// NewEntRefStore builds a RefStore over an ent client.
func NewEntRefStore(db *ent.Client) *EntRefStore {
	return &EntRefStore{db: db}
}

// ReferencedPoints returns the subset of ids that some chunk's qdrant_point_id
// still holds.
func (s *EntRefStore) ReferencedPoints(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.db.Chunk.Query().
		Where(chunk.QdrantPointIDIn(ids...)).
		Select(chunk.FieldQdrantPointID).
		All(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "query referenced points")
	}
	out := make(map[uuid.UUID]bool, len(rows))
	for _, r := range rows {
		if r.QdrantPointID != nil {
			out[*r.QdrantPointID] = true
		}
	}
	return out, nil
}

var _ RefStore = (*EntRefStore)(nil)
