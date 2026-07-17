package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/go-faster/errors"
	"github.com/google/uuid"

	"github.com/go-faster/sisyphus/internal/index"
)

// flakyVectorStore fails Delete the first failures times, then succeeds.
type flakyVectorStore struct {
	failures int
	calls    int
}

func (f *flakyVectorStore) Upsert(context.Context, []index.Chunk, [][]float32) error {
	return nil
}

func (f *flakyVectorStore) Delete(context.Context, []uuid.UUID) error {
	f.calls++
	if f.calls <= f.failures {
		return errors.New("qdrant unavailable")
	}
	return nil
}

// fastRetry keeps the backoff out of real time; the interval is only there to
// space out retries in production.
func fastRetry(store VectorStore) (*Pipeline, error) {
	return New(nil, testChunker{}, &fakeEmbedder{}, store, PipelineOptions{
		VectorDeleteInterval: time.Millisecond,
	})
}

func TestDeleteStaleVectorsRetriesTransientFailure(t *testing.T) {
	// The delete runs after the Postgres commit, so giving up on the first
	// error strands the points permanently: this is why it retries.
	store := &flakyVectorStore{failures: 2}
	p, err := fastRetry(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.deleteStaleVectors(t.Context(), []uuid.UUID{uuid.New()}); err != nil {
		t.Fatalf("expected the retry to recover, got %v", err)
	}
	if store.calls != 3 {
		t.Errorf("Delete called %d times, want 3 (2 failures then success)", store.calls)
	}
}

func TestDeleteStaleVectorsGivesUpAfterMaxTries(t *testing.T) {
	store := &flakyVectorStore{failures: 100}
	p, err := fastRetry(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.deleteStaleVectors(t.Context(), []uuid.UUID{uuid.New()}); err == nil {
		t.Fatal("expected an error once retries are exhausted")
	}
	if store.calls != defaultVectorDeleteTries {
		t.Errorf("Delete called %d times, want %d", store.calls, defaultVectorDeleteTries)
	}
}

func TestDeleteStaleVectorsSucceedsFirstTry(t *testing.T) {
	store := &flakyVectorStore{}
	p, err := fastRetry(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.deleteStaleVectors(t.Context(), []uuid.UUID{uuid.New()}); err != nil {
		t.Fatal(err)
	}
	if store.calls != 1 {
		t.Errorf("Delete called %d times, want 1: a success must not retry", store.calls)
	}
}
