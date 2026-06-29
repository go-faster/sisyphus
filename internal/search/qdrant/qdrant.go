// Package qdrant implements vector search using Qdrant as the backend.
// It provides a thin wrapper around the Qdrant client for managing collections
// and performing similarity searches with embeddings.
package qdrant

import (
	"context"

	"github.com/go-faster/errors"
	"github.com/google/uuid"
	"github.com/qdrant/go-client/qdrant"

	"github.com/go-faster/scpbot/internal/index"
)

// Config holds Qdrant client configuration.
type Config struct {
	// Host is the Qdrant server hostname, defaults to "localhost".
	Host string
	// Port is the gRPC port, defaults to 6334.
	Port int
	// Collection is the collection name to use for searches.
	Collection string
	// Dim is the vector dimension for this collection.
	Dim int
	// Embedder produces embeddings for queries.
	Embedder index.Embedder
}

// Store wraps a Qdrant client for vector search.
type Store struct {
	client     *qdrant.Client
	collection string
	dim        int
	embedder   index.Embedder
}

// New creates a new Qdrant Store.
func New(cfg Config) (*Store, error) {
	if cfg.Host == "" {
		cfg.Host = "localhost"
	}
	if cfg.Port == 0 {
		cfg.Port = 6334
	}

	client, err := qdrant.NewClient(&qdrant.Config{
		Host: cfg.Host,
		Port: cfg.Port,
	})
	if err != nil {
		return nil, errors.Wrap(err, "create qdrant client")
	}

	return &Store{
		client:     client,
		collection: cfg.Collection,
		dim:        cfg.Dim,
		embedder:   cfg.Embedder,
	}, nil
}

// EnsureCollection creates the collection if it does not exist.
// It is idempotent—calling it multiple times is safe.
func (s *Store) EnsureCollection(ctx context.Context) error {
	exists, err := s.client.CollectionExists(ctx, s.collection)
	if err != nil {
		return errors.Wrap(err, "check collection exists")
	}
	if exists {
		return nil
	}

	vectorParams := &qdrant.VectorParams{
		Size:     uint64(s.dim),
		Distance: qdrant.Distance_Cosine,
	}

	createReq := &qdrant.CreateCollection{
		CollectionName: s.collection,
		VectorsConfig:  qdrant.NewVectorsConfig(vectorParams),
	}

	if err := s.client.CreateCollection(ctx, createReq); err != nil {
		return errors.Wrap(err, "create collection")
	}
	return nil
}

// Upsert uploads chunks and their embeddings to Qdrant.
// vectors[i] corresponds to chunks[i].
func (s *Store) Upsert(ctx context.Context, chunks []index.Chunk, vectors [][]float32) error {
	if len(chunks) != len(vectors) {
		return errors.New("chunks and vectors length mismatch")
	}

	points := make([]*qdrant.PointStruct, len(chunks))
	for i, chunk := range chunks {
		payload := chunkToPayload(chunk)

		points[i] = &qdrant.PointStruct{
			Id:      qdrant.NewIDUUID(chunk.ID.String()),
			Payload: payload,
			Vectors: qdrant.NewVectorsDense(vectors[i]),
		}
	}

	wait := true
	req := &qdrant.UpsertPoints{
		CollectionName: s.collection,
		Points:         points,
		Wait:           &wait,
	}
	if _, err := s.client.Upsert(ctx, req); err != nil {
		return errors.Wrap(err, "upsert points")
	}
	return nil
}

// Delete removes a set of points (by chunk ID) from the Qdrant collection.
func (s *Store) Delete(ctx context.Context, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	points := make([]*qdrant.PointId, len(ids))
	for i, id := range ids {
		points[i] = qdrant.NewIDUUID(id.String())
	}
	wait := true
	req := &qdrant.DeletePoints{
		CollectionName: s.collection,
		Wait:           &wait,
		Points:         qdrant.NewPointsSelector(points...),
	}
	if _, err := s.client.Delete(ctx, req); err != nil {
		return errors.Wrap(err, "delete points")
	}
	return nil
}

// Search performs a vector search query.
// It embeds the query text, searches Qdrant, and returns results.
func (s *Store) Search(ctx context.Context, q index.Query) ([]index.Result, error) {
	if q.Limit <= 0 {
		q.Limit = 30
	}

	// Embed the query text
	embeddings, err := s.embedder.Embed(ctx, []string{q.Text})
	if err != nil {
		return nil, errors.Wrap(err, "embed query")
	}
	if len(embeddings) == 0 {
		return nil, errors.New("no embeddings returned")
	}

	queryVec := embeddings[0]

	// Build filter from Query.Service and Query.Filters
	filter := buildFilter(q)

	// Perform the query
	query := qdrant.NewQueryDense(queryVec)
	limit := uint64(q.Limit)
	req := &qdrant.QueryPoints{
		CollectionName: s.collection,
		Query:          query,
		Limit:          &limit,
		Filter:         filter,
		WithPayload:    &qdrant.WithPayloadSelector{SelectorOptions: &qdrant.WithPayloadSelector_Enable{Enable: true}},
	}

	scoredPoints, err := s.client.Query(ctx, req)
	if err != nil {
		return nil, errors.Wrap(err, "query qdrant")
	}

	// Convert scored points to Results
	results := make([]index.Result, len(scoredPoints))
	for i, sp := range scoredPoints {
		chunk := payloadToChunk(sp.Id, sp.Payload)
		results[i] = index.Result{
			Chunk:  chunk,
			Score:  float64(sp.Score),
			Vector: true,
		}
	}

	return results, nil
}

// Close closes the Qdrant client connection.
func (s *Store) Close() error {
	if s.client == nil {
		return nil
	}
	return s.client.Close()
}

// chunkToPayload converts a Chunk to a Qdrant payload map.
// It extracts metadata fields into the top-level payload.
func chunkToPayload(chunk index.Chunk) map[string]*qdrant.Value {
	payload := qdrant.NewValueMap(map[string]any{
		"chunk_id":    chunk.ID.String(),
		"document_id": chunk.DocumentID.String(),
		"chunk_type":  string(chunk.Type),
		"title":       chunk.Title,
	})

	// If metadata exists, merge its fields into the payload
	if chunk.Metadata != nil {
		for k, v := range chunk.Metadata {
			// Convert v to a Qdrant Value if it's not already
			val, err := qdrant.NewValue(v)
			if err == nil && val != nil {
				payload[k] = val
			}
		}
	}

	return payload
}

// payloadToChunk reconstructs a Chunk from a Qdrant payload.
func payloadToChunk(_ *qdrant.PointId, payload map[string]*qdrant.Value) index.Chunk {
	chunk := index.Chunk{
		Metadata: make(map[string]any),
	}

	// Extract chunk_id and document_id as UUIDs
	if v, ok := payload["chunk_id"]; ok && v.GetStringValue() != "" {
		id, err := uuid.Parse(v.GetStringValue())
		if err == nil {
			chunk.ID = id
		}
	}

	if v, ok := payload["document_id"]; ok && v.GetStringValue() != "" {
		id, err := uuid.Parse(v.GetStringValue())
		if err == nil {
			chunk.DocumentID = id
		}
	}

	// Extract scalar fields
	if v, ok := payload["chunk_type"]; ok {
		chunk.Type = index.ChunkType(v.GetStringValue())
	}
	if v, ok := payload["title"]; ok {
		chunk.Title = v.GetStringValue()
	}

	// Remaining fields become metadata
	knownKeys := map[string]bool{
		"chunk_id":    true,
		"document_id": true,
		"chunk_type":  true,
		"title":       true,
	}

	for k, v := range payload {
		if !knownKeys[k] {
			chunk.Metadata[k] = valueToAny(v)
		}
	}

	return chunk
}

// valueToAny converts a Qdrant Value to a Go any.
func valueToAny(v *qdrant.Value) any {
	if v == nil {
		return nil
	}

	switch v.Kind.(type) {
	case *qdrant.Value_NullValue:
		return nil
	case *qdrant.Value_DoubleValue:
		return v.GetDoubleValue()
	case *qdrant.Value_IntegerValue:
		return v.GetIntegerValue()
	case *qdrant.Value_StringValue:
		return v.GetStringValue()
	case *qdrant.Value_BoolValue:
		return v.GetBoolValue()
	default:
		return nil
	}
}

// buildFilter constructs a Qdrant Filter from Query.Service and Query.Filters.
func buildFilter(q index.Query) *qdrant.Filter {
	if q.Service == "" && len(q.Filters) == 0 {
		return nil
	}

	var conditions []*qdrant.Condition

	// Add service filter if present
	if q.Service != "" {
		conditions = append(conditions, qdrant.NewMatchKeyword("service", q.Service))
	}

	// Add filters from Query.Filters
	for k, v := range q.Filters {
		// Convert v to a string or appropriate filter type
		if sv, ok := v.(string); ok {
			conditions = append(conditions, qdrant.NewMatchKeyword(k, sv))
		}
	}

	if len(conditions) == 0 {
		return nil
	}

	return &qdrant.Filter{
		Must: conditions,
	}
}
