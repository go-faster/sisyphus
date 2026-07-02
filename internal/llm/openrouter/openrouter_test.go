package openrouter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openai/openai-go/v3"

	"github.com/go-faster/sisyphus/internal/index"
)

// fakeCompletion replies with a fixed content string for any chat/completions request.
func fakeCompletion(t *testing.T, content string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") == "" {
			t.Error("missing Authorization header")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		resp := openai.ChatCompletion{
			Choices: []openai.ChatCompletionChoice{
				{Message: openai.ChatCompletionMessage{Content: content}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	return New("test-key", Options{BaseURL: srv.URL})
}

func TestSummarizer_HappyPath(t *testing.T) {
	srv := fakeCompletion(t, "A concise summary.")
	s := NewSummarizer(newClient(t, srv), "test-model", SummarizerOptions{})

	got, err := s.Summarize(context.Background(), "some long text")
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if got != "A concise summary." {
		t.Errorf("unexpected summary: %q", got)
	}
}

func TestAnswerer_HappyPath(t *testing.T) {
	srv := fakeCompletion(t, "The answer is 42.")
	a := NewAnswerer(newClient(t, srv), "test-model", AnswererOptions{})

	results := []index.Result{
		{Chunk: index.Chunk{Title: "Doc", Text: "The answer is 42."}},
	}
	got, err := a.Answer(context.Background(), "What is the answer?", results)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if got != "The answer is 42." {
		t.Errorf("unexpected answer: %q", got)
	}
}

func TestAnswerer_EmptyResults(t *testing.T) {
	srv := fakeCompletion(t, "I don't have enough context.")
	a := NewAnswerer(newClient(t, srv), "test-model", AnswererOptions{})

	_, err := a.Answer(context.Background(), "What is the answer?", nil)
	if err != nil {
		t.Fatalf("Answer with empty results: %v", err)
	}
}

func TestClient_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	s := NewSummarizer(newClient(t, srv), "test-model", SummarizerOptions{})
	_, err := s.Summarize(context.Background(), "text")
	if err == nil {
		t.Fatal("expected error from HTTP 500")
	}
}

func TestClient_NoChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := openai.ChatCompletion{Choices: nil}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)

	s := NewSummarizer(newClient(t, srv), "test-model", SummarizerOptions{})
	_, err := s.Summarize(context.Background(), "text")
	if err == nil {
		t.Fatal("expected error when no choices returned")
	}
}

func TestClient_CanceledContext(t *testing.T) {
	srv := fakeCompletion(t, "ok")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s := NewSummarizer(newClient(t, srv), "test-model", SummarizerOptions{})
	_, err := s.Summarize(ctx, "text")
	if err == nil {
		t.Fatal("expected error from canceled context")
	}
}
