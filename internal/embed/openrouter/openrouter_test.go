package openrouter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmbedder_Embed(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want bearer token", got)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		var req struct {
			Input []string `json:"input"`
			Model string   `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if req.Model != "test-embed" {
			t.Errorf("model = %q, want test-embed", req.Model)
		}
		if len(req.Input) != 2 || req.Input[0] != "first" || req.Input[1] != "second" {
			t.Errorf("input = %#v, want ordered inputs", req.Input)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"model":  req.Model,
			"data": []map[string]any{
				{"object": "embedding", "index": 1, "embedding": []float64{3, 4}},
				{"object": "embedding", "index": 0, "embedding": []float64{1, 2}},
			},
			"usage": map[string]any{"prompt_tokens": 1, "total_tokens": 1},
		})
	}))
	t.Cleanup(srv.Close)

	e := New("test-key", "test-embed", EmbedderOptions{BaseURL: srv.URL, Dim: 2})
	got, err := e.Embed(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if e.Dim() != 2 {
		t.Fatalf("Dim = %d, want 2", e.Dim())
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0][0] != 1 || got[0][1] != 2 || got[1][0] != 3 || got[1][1] != 4 {
		t.Fatalf("embeddings = %#v, want response ordered by index", got)
	}
}

func TestEmbedder_EmptyInput(t *testing.T) {
	t.Parallel()

	e := New("test-key", "test-embed", EmbedderOptions{})
	got, err := e.Embed(context.Background(), nil)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if got != nil {
		t.Fatalf("got %#v, want nil", got)
	}
}

func TestEmbedder_InvalidIndex(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"model":  "test-embed",
			"data": []map[string]any{
				{"object": "embedding", "index": 1, "embedding": []float64{1, 2}},
			},
			"usage": map[string]any{"prompt_tokens": 1, "total_tokens": 1},
		})
	}))
	t.Cleanup(srv.Close)

	e := New("test-key", "test-embed", EmbedderOptions{BaseURL: srv.URL})
	_, err := e.Embed(context.Background(), []string{"only"})
	if err == nil {
		t.Fatal("expected invalid index error")
	}
}
