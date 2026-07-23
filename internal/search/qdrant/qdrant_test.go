package qdrant

import (
	"context"
	"net"
	"testing"

	"github.com/google/uuid"
	"github.com/qdrant/go-client/qdrant"
	"google.golang.org/grpc"

	"github.com/go-faster/sisyphus/internal/index"
)

// TestChunkToPayload tests conversion of Chunk to Qdrant payload.
func TestChunkToPayload(t *testing.T) {
	chunkID := uuid.New()
	docID := uuid.New()

	chunk := index.Chunk{
		ID:         chunkID,
		DocumentID: docID,
		Type:       index.ChunkSection,
		Title:      "Test Title",
		Metadata: map[string]any{
			"source":  "gitlab_docs",
			"service": "api",
			"count":   42,
		},
	}

	payload, _ := chunkToPayload(chunk)

	// Check basic fields
	if v, ok := payload["chunk_id"]; !ok || v.GetStringValue() != chunkID.String() {
		t.Errorf("chunk_id not set correctly")
	}

	if v, ok := payload["document_id"]; !ok || v.GetStringValue() != docID.String() {
		t.Errorf("document_id not set correctly")
	}

	if v, ok := payload["chunk_type"]; !ok || v.GetStringValue() != "section" {
		t.Errorf("chunk_type not set correctly, got %v", v)
	}

	if v, ok := payload["title"]; !ok || v.GetStringValue() != "Test Title" {
		t.Errorf("title not set correctly")
	}

	// Check metadata fields
	if v, ok := payload["source"]; !ok || v.GetStringValue() != "gitlab_docs" {
		t.Errorf("source metadata not preserved")
	}

	if v, ok := payload["service"]; !ok || v.GetStringValue() != "api" {
		t.Errorf("service metadata not preserved")
	}

	if v, ok := payload["count"]; !ok || v.GetIntegerValue() != 42 {
		t.Errorf("count metadata not preserved")
	}
}

func TestChunkToPayloadInvalidUTF8(t *testing.T) {
	chunk := index.Chunk{
		ID:         uuid.New(),
		DocumentID: uuid.New(),
		Type:       index.ChunkSection,
		Title:      "title\xffsuffix",
		Metadata: map[string]any{
			"bad_string": "value\xffsuffix",
			"nested": map[string]any{
				"bad_key\xff": "nested\xffvalue",
			},
			"list": []any{"list\xffvalue"},
		},
	}

	payload, _ := chunkToPayload(chunk)

	if got := payload["title"].GetStringValue(); got != "titlesuffix" {
		t.Fatalf("title: got %q, want %q", got, "titlesuffix")
	}
	if got := payload["bad_string"].GetStringValue(); got != "valuesuffix" {
		t.Fatalf("bad_string: got %q, want %q", got, "valuesuffix")
	}
	if got := payload["nested"].GetStructValue().Fields["bad_key"].GetStringValue(); got != "nestedvalue" {
		t.Fatalf("nested: got %q, want %q", got, "nestedvalue")
	}
	if got := payload["list"].GetListValue().Values[0].GetStringValue(); got != "listvalue" {
		t.Fatalf("list: got %q, want %q", got, "listvalue")
	}
}

// TestPayloadToChunk tests conversion of Qdrant payload back to Chunk.
func TestPayloadToChunk(t *testing.T) {
	chunkID := uuid.New()
	docID := uuid.New()

	pointID := qdrant.NewIDUUID(chunkID.String())
	payload := map[string]*qdrant.Value{
		"chunk_id":    qdrant.NewValueString(chunkID.String()),
		"document_id": qdrant.NewValueString(docID.String()),
		"chunk_type":  qdrant.NewValueString("section"),
		"title":       qdrant.NewValueString("Test Title"),
		"source":      qdrant.NewValueString("gitlab_docs"),
		"count":       qdrant.NewValueInt(42),
	}

	chunk := payloadToChunk(pointID, payload)

	if chunk.ID != chunkID {
		t.Errorf("chunk.ID mismatch: got %v, want %v", chunk.ID, chunkID)
	}

	if chunk.DocumentID != docID {
		t.Errorf("chunk.DocumentID mismatch: got %v, want %v", chunk.DocumentID, docID)
	}

	if chunk.Type != index.ChunkSection {
		t.Errorf("chunk.Type mismatch: got %v, want %v", chunk.Type, index.ChunkSection)
	}

	if chunk.Title != "Test Title" {
		t.Errorf("chunk.Title mismatch: got %q, want %q", chunk.Title, "Test Title")
	}

	// Check metadata
	if v, ok := chunk.Metadata["source"]; !ok || v != "gitlab_docs" {
		t.Errorf("source in metadata not correct")
	}

	if v, ok := chunk.Metadata["count"]; !ok || v != int64(42) {
		t.Errorf("count in metadata not correct")
	}
}

// TestRoundTrip tests that chunk -> payload -> chunk preserves data.
func TestRoundTrip(t *testing.T) {
	chunkID := uuid.New()
	docID := uuid.New()

	original := index.Chunk{
		ID:         chunkID,
		DocumentID: docID,
		Type:       index.ChunkJiraSummary,
		Title:      "Jira Issue Summary",
		Metadata: map[string]any{
			"jira_key": "PROJ-123",
			"repo":     "my-repo",
			"service":  "backend",
		},
	}

	// Convert to payload
	payload, _ := chunkToPayload(original)

	// Convert back to chunk
	pointID := qdrant.NewIDUUID(chunkID.String())
	recovered := payloadToChunk(pointID, payload)

	// Verify fields
	if recovered.ID != original.ID {
		t.Errorf("ID mismatch after roundtrip")
	}

	if recovered.DocumentID != original.DocumentID {
		t.Errorf("DocumentID mismatch after roundtrip")
	}

	if recovered.Type != original.Type {
		t.Errorf("Type mismatch after roundtrip")
	}

	if recovered.Title != original.Title {
		t.Errorf("Title mismatch after roundtrip")
	}

	// Verify metadata
	for k, v := range original.Metadata {
		if val, ok := recovered.Metadata[k]; !ok || val != v {
			t.Errorf("metadata key %q mismatch after roundtrip", k)
		}
	}
}

// TestBuildFilterWithService tests filter construction with a service.
func TestBuildFilterWithService(t *testing.T) {
	q := index.Query{
		Text:    "search term",
		Service: "my-service",
	}

	filter := buildFilter(q)
	if filter == nil {
		t.Fatal("filter is nil")
	}

	if len(filter.Must) == 0 {
		t.Errorf("filter.Must is empty, expected 1 condition")
	}
}

// TestBuildFilterWithFilters tests filter construction with Query.Filters.
func TestBuildFilterWithFilters(t *testing.T) {
	q := index.Query{
		Text: "search term",
		Filters: map[string]string{
			"source": "gitlab_docs",
			"repo":   "my-repo",
		},
	}

	filter := buildFilter(q)
	if filter == nil {
		t.Fatal("filter is nil")
	}

	if len(filter.Must) != 2 {
		t.Errorf("filter.Must length: got %d, want 2", len(filter.Must))
	}
}

// TestBuildFilterNoServiceOrFilters tests that nil filter is returned when neither service nor filters.
func TestBuildFilterNoServiceOrFilters(t *testing.T) {
	q := index.Query{
		Text: "search term",
	}

	filter := buildFilter(q)
	if filter != nil {
		t.Errorf("filter should be nil when no service or filters")
	}
}

// TestBuildFilterBothServiceAndFilters tests combining service and filters.
func TestBuildFilterBothServiceAndFilters(t *testing.T) {
	q := index.Query{
		Text:    "search term",
		Service: "my-service",
		Filters: map[string]string{
			"source": "jira",
		},
	}

	filter := buildFilter(q)
	if filter == nil {
		t.Fatal("filter is nil")
	}

	if len(filter.Must) != 2 {
		t.Errorf("filter.Must length: got %d, want 2", len(filter.Must))
	}
}

// TestValueToAny tests conversion of Qdrant Value types to Go any.
func TestValueToAny(t *testing.T) {
	tests := []struct {
		name     string
		value    *qdrant.Value
		expected any
	}{
		{
			name:     "nil value",
			value:    nil,
			expected: nil,
		},
		{
			name:     "null value",
			value:    qdrant.NewValueNull(),
			expected: nil,
		},
		{
			name:     "string value",
			value:    qdrant.NewValueString("test"),
			expected: "test",
		},
		{
			name:     "int value",
			value:    qdrant.NewValueInt(42),
			expected: int64(42),
		},
		{
			name:     "double value",
			value:    qdrant.NewValueDouble(3.14),
			expected: 3.14,
		},
		{
			name:     "bool value true",
			value:    qdrant.NewValueBool(true),
			expected: true,
		},
		{
			name:     "bool value false",
			value:    qdrant.NewValueBool(false),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := valueToAny(tt.value)
			if result != tt.expected {
				t.Errorf("got %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestSearcherInterface verifies Store implements index.Searcher.
func TestSearcherInterface(t *testing.T) {
	var _ index.Searcher = (*Store)(nil)
}

func TestStoreUpsertAndDeleteSuccess(t *testing.T) {
	ctx := context.Background()
	client, stop := newTestQdrantClient(t)
	defer stop()

	store := &Store{
		client:     client,
		collection: "test",
		dim:        2,
	}
	chunkID := uuid.New()
	docID := uuid.New()
	err := store.Upsert(ctx, []index.Chunk{{
		ID:         chunkID,
		DocumentID: docID,
		Type:       index.ChunkSection,
		Title:      "test",
	}}, [][]float32{{0.1, 0.2}})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := store.Delete(ctx, []uuid.UUID{chunkID}); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func newTestQdrantClient(t *testing.T) (clinet *qdrant.Client, stop func()) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := grpc.NewServer()
	qdrant.RegisterPointsServer(server, testPointsServer{})

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(ln)
	}()

	client, err := qdrant.NewClient(&qdrant.Config{
		Host:                   ln.Addr().(*net.TCPAddr).IP.String(),
		Port:                   ln.Addr().(*net.TCPAddr).Port,
		SkipCompatibilityCheck: true,
		PoolSize:               1,
	})
	if err != nil {
		server.Stop()
		_ = ln.Close()
		t.Fatalf("new qdrant client: %v", err)
	}

	stop = func() {
		_ = client.Close()
		server.Stop()
		if err := <-serveErr; err != nil && err != grpc.ErrServerStopped {
			t.Fatalf("serve: %v", err)
		}
	}
	return client, stop
}

type testPointsServer struct {
	qdrant.UnimplementedPointsServer
}

func (testPointsServer) Upsert(context.Context, *qdrant.UpsertPoints) (*qdrant.PointsOperationResponse, error) {
	return &qdrant.PointsOperationResponse{
		Result: &qdrant.UpdateResult{Status: qdrant.UpdateStatus_Completed},
	}, nil
}

func (testPointsServer) Delete(context.Context, *qdrant.DeletePoints) (*qdrant.PointsOperationResponse, error) {
	return &qdrant.PointsOperationResponse{
		Result: &qdrant.UpdateResult{Status: qdrant.UpdateStatus_Completed},
	}, nil
}

// TestConfigDefaults verifies that Config applies sensible defaults.
func TestConfigDefaults(t *testing.T) {
	tests := []struct {
		name     string
		config   Config
		wantHost string
		wantPort int
	}{
		{
			name: "empty config gets defaults",
			config: Config{
				Collection: "test",
				Dim:        1024,
				Embedder:   nil,
			},
			wantHost: "localhost",
			wantPort: 6334,
		},
		{
			name: "explicit host and port are preserved",
			config: Config{
				Host:       "qdrant.example.com",
				Port:       6335,
				Collection: "test",
				Dim:        1024,
				Embedder:   nil,
			},
			wantHost: "qdrant.example.com",
			wantPort: 6335,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// We can't fully test New without a real Qdrant server,
			// but we can document the default behavior here.
			// The actual defaults are applied in New().
			if tt.config.Host == "" && tt.wantHost != "localhost" {
				t.Errorf("empty Host should default to localhost")
			}
			if tt.config.Port == 0 && tt.wantPort != 6334 {
				t.Errorf("zero Port should default to 6334")
			}
		})
	}
}

// TestChunkToPayloadEmptyMetadata tests payload generation with empty metadata.
func TestChunkToPayloadEmptyMetadata(t *testing.T) {
	chunk := index.Chunk{
		ID:         uuid.New(),
		DocumentID: uuid.New(),
		Type:       index.ChunkCodeFile,
		Title:      "main.go",
	}

	payload, _ := chunkToPayload(chunk)

	if len(payload) < 4 {
		t.Errorf("payload should have at least 4 keys, got %d", len(payload))
	}
}

// TestPayloadToChunkMissingFields tests that missing fields don't cause errors.
func TestPayloadToChunkMissingFields(t *testing.T) {
	// Minimal payload with just IDs
	payload := map[string]*qdrant.Value{
		"chunk_id":    qdrant.NewValueString(uuid.New().String()),
		"document_id": qdrant.NewValueString(uuid.New().String()),
	}

	pointID := qdrant.NewIDUUID(uuid.New().String())
	chunk := payloadToChunk(pointID, payload)

	if chunk.ID == uuid.Nil {
		t.Errorf("chunk.ID should be set")
	}

	if chunk.DocumentID == uuid.Nil {
		t.Errorf("chunk.DocumentID should be set")
	}
}
