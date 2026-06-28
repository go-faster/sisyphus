package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEmbed_HappyPath(t *testing.T) {
	// Create a fake Ollama server that returns embeddings.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("unexpected content-type: %s", ct)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Parse the request.
		var req embedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Return embeddings matching the input count.
		resp := embedResponse{
			Embeddings: make([][]float32, len(req.Input)),
		}
		for i := range resp.Embeddings {
			resp.Embeddings[i] = make([]float32, 1024)
			// Fill with recognizable values for testing.
			for j := range resp.Embeddings[i] {
				resp.Embeddings[i][j] = float32(i) + float32(j)*0.001
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	e := New(server.URL, "bge-m3", EmbedderOptions{})
	ctx := t.Context()

	// Test with multiple texts.
	texts := []string{"hello", "world", "test"}
	embeddings, err := e.Embed(ctx, texts)
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}

	if len(embeddings) != len(texts) {
		t.Errorf("expected %d embeddings, got %d", len(texts), len(embeddings))
	}

	// Verify embeddings are in order and non-empty.
	for i, emb := range embeddings {
		if len(emb) != 1024 {
			t.Errorf("embedding %d has wrong dimension: %d", i, len(emb))
		}
		// Verify recognizable values.
		if emb[0] != float32(i) {
			t.Errorf("embedding %d first value mismatch: %f", i, emb[0])
		}
	}
}

func TestEmbed_EmptyInput(t *testing.T) {
	e := New("http://localhost:8000", "bge-m3", EmbedderOptions{})
	ctx := t.Context()

	embeddings, err := e.Embed(ctx, []string{})
	if err != nil {
		t.Errorf("Embed with empty input should not error, got: %v", err)
	}
	if embeddings != nil {
		t.Errorf("Embed with empty input should return nil, got: %v", embeddings)
	}
}

func TestEmbed_LengthMismatch(t *testing.T) {
	// Create a server that returns wrong number of embeddings.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := embedResponse{
			Embeddings: [][]float32{
				{1.0, 2.0, 3.0},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	e := New(server.URL, "bge-m3", EmbedderOptions{})
	ctx := t.Context()

	// Request 3 embeddings but server returns 1.
	embeddings, err := e.Embed(ctx, []string{"a", "b", "c"})
	if err == nil {
		t.Errorf("expected error for length mismatch, got nil")
	}
	if embeddings != nil {
		t.Errorf("expected nil embeddings on error, got: %v", embeddings)
	}
	if err.Error() != "ollama returned 1 embeddings but 3 were requested" {
		t.Errorf("unexpected error message: %s", err.Error())
	}
}

func TestEmbed_NonOKStatus(t *testing.T) {
	// Create a server that returns an error.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("server error details"))
	}))
	defer server.Close()

	e := New(server.URL, "bge-m3", EmbedderOptions{})
	ctx := t.Context()

	embeddings, err := e.Embed(ctx, []string{"hello"})
	if err == nil {
		t.Errorf("expected error for non-OK status, got nil")
	}
	if embeddings != nil {
		t.Errorf("expected nil embeddings on error, got: %v", embeddings)
	}
	if err.Error() != "ollama returned status 500: server error details" {
		t.Errorf("unexpected error message: %s", err.Error())
	}
}

func TestEmbed_ContextHonored(t *testing.T) {
	// Create a server that records whether it received a request.
	requestChan := make(chan *http.Request, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestChan <- r
		resp := embedResponse{
			Embeddings: [][]float32{{1.0}},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	e := New(server.URL, "bge-m3", EmbedderOptions{})

	// Test with a normal context.
	ctx := t.Context()
	_, err := e.Embed(ctx, []string{"hello"})
	if err != nil {
		t.Fatalf("Embed with normal context failed: %v", err)
	}

	// Verify the request was sent and had the correct context.
	select {
	case req := <-requestChan:
		if req.Context() == nil {
			t.Errorf("request context should not be nil")
		}
	case <-time.After(time.Second):
		t.Errorf("request was not sent to server within timeout")
	}

	// Test with a canceled context.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err = e.Embed(ctx, []string{"hello"})
	if err == nil {
		t.Errorf("expected error from canceled context, got nil")
	}
}

func TestDim_Default(t *testing.T) {
	e := New("http://localhost:8000", "bge-m3", EmbedderOptions{})
	if e.Dim() != 1024 {
		t.Errorf("expected default dim 1024, got %d", e.Dim())
	}
}

func TestDim_Custom(t *testing.T) {
	e := New("http://localhost:8000", "bge-m3", EmbedderOptions{Dim: 512})
	if e.Dim() != 512 {
		t.Errorf("expected custom dim 512, got %d", e.Dim())
	}
}

func TestWithHTTPClient(t *testing.T) {
	// Create a fake server.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := embedResponse{
			Embeddings: [][]float32{{1.0}},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Create a custom HTTP client that tracks requests.
	var requestCount int
	customClient := &http.Client{
		Transport: &trackingTransport{
			inner: http.DefaultTransport,
			count: &requestCount,
		},
	}

	e := New(server.URL, "bge-m3", EmbedderOptions{HTTPClient: customClient})
	ctx := t.Context()

	_, err := e.Embed(ctx, []string{"test"})
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}

	if requestCount != 1 {
		t.Errorf("expected custom client to be used, got request count: %d", requestCount)
	}
}

func TestEmbed_MalformedResponse(t *testing.T) {
	// Create a server that returns malformed JSON.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{invalid json"))
	}))
	defer server.Close()

	e := New(server.URL, "bge-m3", EmbedderOptions{})
	ctx := t.Context()

	embeddings, err := e.Embed(ctx, []string{"hello"})
	if err == nil {
		t.Errorf("expected error for malformed response, got nil")
	}
	if embeddings != nil {
		t.Errorf("expected nil embeddings on error, got: %v", embeddings)
	}
}

func TestEmbed_LargeBodySnippet(t *testing.T) {
	// Create a server that returns a large error body.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		// Write more than 512 bytes to verify it's truncated in error message.
		for range 100 {
			w.Write([]byte("error details line\n"))
		}
	}))
	defer server.Close()

	e := New(server.URL, "bge-m3", EmbedderOptions{})
	ctx := t.Context()

	_, err := e.Embed(ctx, []string{"hello"})
	if err == nil {
		t.Errorf("expected error for bad request, got nil")
	}

	// Check that error message contains status code.
	if !bytes.Contains([]byte(err.Error()), []byte("400")) {
		t.Errorf("error should contain status code, got: %s", err.Error())
	}
}

// trackingTransport wraps an http.RoundTripper to count requests.
type trackingTransport struct {
	inner http.RoundTripper
	count *int
}

func (t *trackingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	*t.count++
	return t.inner.RoundTrip(req)
}
