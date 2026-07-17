package qdrant

import (
	"context"

	"github.com/go-faster/errors"
	"github.com/google/uuid"
	"github.com/qdrant/go-client/qdrant"
)

// defaultScanBatch is the page size for ScanPointIDs. Point IDs are small, so
// this trades a handful of round trips against holding the whole collection in
// memory at once.
const defaultScanBatch = 1024

// ScanPointIDs walks every point in the collection and hands their IDs to fn in
// batches. It requests neither payload nor vectors, so a full scan stays cheap.
//
// The scroll is not a snapshot: points written while it runs may or may not be
// seen. Callers that act on the absence of a point (garbage collection) must not
// treat a single scan as authoritative — see internal/vectorgc.
func (s *Store) ScanPointIDs(ctx context.Context, batch int, fn func([]uuid.UUID) error) error {
	if batch <= 0 {
		batch = defaultScanBatch
	}
	limit := uint32(batch)
	withPayload := &qdrant.WithPayloadSelector{
		SelectorOptions: &qdrant.WithPayloadSelector_Enable{Enable: false},
	}
	withVectors := &qdrant.WithVectorsSelector{
		SelectorOptions: &qdrant.WithVectorsSelector_Enable{Enable: false},
	}

	var offset *qdrant.PointId
	for {
		points, err := s.client.Scroll(ctx, &qdrant.ScrollPoints{
			CollectionName: s.collection,
			Limit:          &limit,
			Offset:         offset,
			WithPayload:    withPayload,
			WithVectors:    withVectors,
		})
		if err != nil {
			return errors.Wrap(err, "scroll points")
		}
		if len(points) == 0 {
			return nil
		}

		ids := make([]uuid.UUID, 0, len(points))
		for _, p := range points {
			id, err := pointUUID(p.GetId())
			if err != nil {
				// A non-UUID point ID cannot correspond to a chunk. Skip it
				// rather than fail the scan: reporting it as unknown would let a
				// caller delete a point it does not understand.
				continue
			}
			ids = append(ids, id)
		}
		if len(ids) > 0 {
			if err := fn(ids); err != nil {
				return err
			}
		}

		// A short page means the collection is exhausted: Scroll returns the
		// next offset only while more points remain.
		if len(points) < batch {
			return nil
		}
		offset = points[len(points)-1].GetId()
	}
}

// pointUUID extracts the UUID form of a point ID. Chunks are always stored under
// their UUID, so anything else is not ours.
func pointUUID(id *qdrant.PointId) (uuid.UUID, error) {
	if id == nil {
		return uuid.Nil, errors.New("nil point id")
	}
	u, ok := id.GetPointIdOptions().(*qdrant.PointId_Uuid)
	if !ok {
		return uuid.Nil, errors.New("point id is not a uuid")
	}
	parsed, err := uuid.Parse(u.Uuid)
	if err != nil {
		return uuid.Nil, errors.Wrap(err, "parse point id")
	}
	return parsed, nil
}
