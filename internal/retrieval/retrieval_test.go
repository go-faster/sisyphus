package retrieval

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/go-faster/scpbot/internal/index"
)

// fakeSearcher returns a fixed set of results.
type fakeSearcher struct {
	results []index.Result
	err     error
}

func (f fakeSearcher) Search(_ context.Context, _ index.Query) ([]index.Result, error) {
	return f.results, f.err
}

func result(id uuid.UUID, score float64, vector bool, meta map[string]any) index.Result {
	return index.Result{
		Chunk:  index.Chunk{ID: id, Metadata: meta},
		Score:  score,
		Vector: vector,
	}
}

func TestRetrieveMergesAndBoosts(t *testing.T) {
	shared := uuid.New()
	lexOnly := uuid.New()
	vecOnly := uuid.New()

	lexical := fakeSearcher{results: []index.Result{
		result(shared, 1.0, false, map[string]any{"authority": string(index.AuthorityLow)}),
		result(lexOnly, 0.5, false, nil),
	}}
	vector := fakeSearcher{results: []index.Result{
		result(shared, 0.8, true, map[string]any{"authority": string(index.AuthorityLow)}),
		result(vecOnly, 0.9, true, map[string]any{"authority": string(index.AuthorityHigh)}),
	}}

	svc, err := New(lexical, vector)
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.Retrieve(t.Context(), index.Query{Text: "q", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 merged results, got %d", len(got))
	}
	// The shared chunk must appear once (deduped by ID).
	seen := map[uuid.UUID]int{}
	for _, r := range got {
		seen[r.Chunk.ID]++
	}
	if seen[shared] != 1 {
		t.Fatalf("shared chunk not deduped: %d", seen[shared])
	}
}

func TestRetrieveServiceBoost(t *testing.T) {
	a := uuid.New()
	b := uuid.New()
	lexical := fakeSearcher{results: []index.Result{
		result(a, 1.0, false, map[string]any{"service": "other"}),
		result(b, 1.0, false, map[string]any{"service": "billing-api"}),
	}}
	svc, err := New(lexical, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.Retrieve(t.Context(), index.Query{Text: "q", Service: "billing-api"})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Chunk.ID != b {
		t.Fatalf("service match should rank first, got %v", got[0].Chunk.ID)
	}
}

func TestRetrieveSurvivesBackendError(t *testing.T) {
	ok := uuid.New()
	lexical := fakeSearcher{err: context.DeadlineExceeded}
	vector := fakeSearcher{results: []index.Result{result(ok, 0.7, true, nil)}}
	svc, err := New(lexical, vector)
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.Retrieve(t.Context(), index.Query{Text: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Chunk.ID != ok {
		t.Fatalf("expected vector result to survive lexical failure, got %+v", got)
	}
}

func TestNewRequiresSearcher(t *testing.T) {
	if _, err := New(nil, nil); err == nil {
		t.Fatal("expected error when no searcher provided")
	}
}
