package retrieval

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/go-faster/sisyphus/internal/index"
)

// fakeSearcher returns a fixed set of results.
type fakeSearcher struct {
	results []index.Result
	err     error
}

func (f fakeSearcher) Search(_ context.Context, _ index.Query) ([]index.Result, error) {
	return f.results, f.err
}

type fakeChunkFetcher struct {
	chunks map[uuid.UUID]index.Chunk
	err    error
}

type blockingSearcher struct {
	id      uuid.UUID
	started chan<- struct{}
	release <-chan struct{}
}

func (b blockingSearcher) Search(ctx context.Context, _ index.Query) ([]index.Result, error) {
	b.started <- struct{}{}
	select {
	case <-b.release:
		return []index.Result{result(b.id, 1, false, nil)}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (f fakeChunkFetcher) FetchChunks(_ context.Context, _ []uuid.UUID) (map[uuid.UUID]index.Chunk, error) {
	return f.chunks, f.err
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
	// Note: vector results are not in score-descending order; retrieval.applyRRF will sort them.
	vector := fakeSearcher{results: []index.Result{
		result(shared, 0.8, true, map[string]any{"authority": string(index.AuthorityLow)}),
		result(vecOnly, 0.9, true, map[string]any{"authority": string(index.AuthorityHigh)}),
	}}

	svc, err := New(lexical, vector, nil, ServiceOptions{})
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

	// Verify RRF ranking with expected scores.
	// After sorting backends by score descending:
	// Lexical: [shared (1.0), lexOnly (0.5)]
	// Vector: [vecOnly (0.9), shared (0.8)]
	//
	// RRF contributions (1 / (60 + rank+1)):
	// - shared: 1/61 (lexical rank 0) + 1/62 (vector rank 1) ≈ 0.01639 + 0.01613 ≈ 0.03252
	//   final: 0.03252 * 0.85 (low authority) ≈ 0.02764
	// - lexOnly: 1/62 (lexical rank 1) ≈ 0.01613
	//   final: 0.01613 * 1.0 (no authority) ≈ 0.01613
	// - vecOnly: 1/61 (vector rank 0) ≈ 0.01639
	//   final: 0.01639 * 1.4 (high authority) ≈ 0.02295
	//
	// Expected order: shared > vecOnly > lexOnly
	if got[0].Chunk.ID != shared {
		t.Fatalf("expected shared to rank first, got %v", got[0].Chunk.ID)
	}
	if got[1].Chunk.ID != vecOnly {
		t.Fatalf("expected vecOnly to rank second, got %v (score: %f)", got[1].Chunk.ID, got[1].Score)
	}
	if got[2].Chunk.ID != lexOnly {
		t.Fatalf("expected lexOnly to rank third, got %v", got[2].Chunk.ID)
	}
}

func TestRetrieveServiceBoost(t *testing.T) {
	a := uuid.New()
	b := uuid.New()
	lexical := fakeSearcher{results: []index.Result{
		result(a, 1.0, false, map[string]any{"service": "other"}),
		result(b, 1.0, false, map[string]any{"service": "billing-api"}),
	}}
	svc, err := New(lexical, nil, nil, ServiceOptions{})
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

func TestRetrieveDedupesSameContent(t *testing.T) {
	keep := uuid.New()
	dupe := uuid.New()
	other := uuid.New()
	hash := index.Hash("same content")
	lexical := fakeSearcher{results: []index.Result{
		{
			Chunk: index.Chunk{ID: keep, Text: "same content", TextHash: hash},
			Score: 1.0,
		},
		{
			Chunk: index.Chunk{ID: dupe, Text: "same content", TextHash: hash},
			Score: 0.9,
		},
		{
			Chunk: index.Chunk{ID: other, Text: "different content", TextHash: index.Hash("different content")},
			Score: 0.8,
		},
	}}
	svc, err := New(lexical, nil, nil, ServiceOptions{})
	if err != nil {
		t.Fatal(err)
	}

	got, err := svc.Retrieve(t.Context(), index.Query{Text: "content", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 deduped results, got %d", len(got))
	}
	if got[0].Chunk.ID != keep {
		t.Fatalf("highest-ranked duplicate should survive, got %v", got[0].Chunk.ID)
	}
	for _, r := range got {
		if r.Chunk.ID == dupe {
			t.Fatal("duplicate content survived")
		}
	}
}

func TestContentKeyNormalizesWhitespace(t *testing.T) {
	a := contentKey(index.Chunk{Text: "Same\n\tcontent"})
	b := contentKey(index.Chunk{Text: "same content"})
	if a == "" || a != b {
		t.Fatalf("normalized keys differ: %q != %q", a, b)
	}
}

func TestRetrieveSurvivesBackendError(t *testing.T) {
	ok := uuid.New()
	lexical := fakeSearcher{err: context.DeadlineExceeded}
	vector := fakeSearcher{results: []index.Result{result(ok, 0.7, true, nil)}}
	svc, err := New(lexical, vector, nil, ServiceOptions{})
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

func TestRetrieveRunsBackendsConcurrently(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	svc, err := New(
		blockingSearcher{id: uuid.New(), started: started, release: release},
		blockingSearcher{id: uuid.New(), started: started, release: release},
		nil,
		ServiceOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := svc.Retrieve(ctx, index.Query{Text: "q"})
		done <- err
	}()

	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			cancel()
			t.Fatal("search backends did not start concurrently")
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestNewRequiresSearcher(t *testing.T) {
	if _, err := New(nil, nil, nil, ServiceOptions{}); err == nil {
		t.Fatal("expected error when no searcher provided")
	}
}

func TestRetrieveHydratesVectorOnlyText(t *testing.T) {
	id := uuid.New()
	vector := fakeSearcher{results: []index.Result{
		result(id, 0.7, true, nil),
	}}
	fetcher := fakeChunkFetcher{chunks: map[uuid.UUID]index.Chunk{
		id: {
			ID:         id,
			Text:       "hydrated text",
			TokenCount: 42,
		},
	}}
	svc, err := New(nil, vector, fetcher, ServiceOptions{})
	if err != nil {
		t.Fatal(err)
	}

	got, err := svc.Retrieve(t.Context(), index.Query{Text: "semantic", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if got[0].Chunk.Text != "hydrated text" {
		t.Fatalf("text: expected hydrated text, got %q", got[0].Chunk.Text)
	}
	if got[0].Chunk.TokenCount != 42 {
		t.Fatalf("token count: expected 42, got %d", got[0].Chunk.TokenCount)
	}
}

func TestRetrievalRRFFixesScaleMismatchBug(t *testing.T) {
	// Regression test for the scale-mismatch bug:
	// A chunk with a huge unbounded ts_rank score (from lexical search) but no
	// vector ranking should not dominate a chunk that ranks well (#1) in the vector
	// backend and has modest ranking in lexical. With RRF (rank-based fusion), the
	// vector-ranking winner should outrank the raw-score winner.

	highTSRank := uuid.New()
	genuinelyRelevant := uuid.New()

	lexical := fakeSearcher{results: []index.Result{
		result(highTSRank, 1000.0, false, nil),     // Huge unbounded ts_rank
		result(genuinelyRelevant, 0.1, false, nil), // Also in lexical, but low score
	}}
	vector := fakeSearcher{results: []index.Result{
		result(genuinelyRelevant, 0.95, true, nil), // #1 in vector (genuinely relevant)
		// highTSRank is not in vector results at all
	}}

	svc, err := New(lexical, vector, nil, ServiceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.Retrieve(t.Context(), index.Query{Text: "q", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}

	if len(got) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(got))
	}

	// With RRF, the chunk that ranks #1 in vector should outrank the chunk that
	// has high raw ts_rank but poor vector ranking.
	// RRF scores (before boost):
	// - highTSRank:      1/(60+1) ≈ 0.01639 (lexical rank 0 only)
	// - genuinelyRelevant: 1/(60+2) + 1/(60+1) ≈ 0.01613 + 0.01639 ≈ 0.03252 (lexical rank 1 + vector rank 0)
	// genuinelyRelevant wins with RRF, proving the scale-mismatch fix.
	if got[0].Chunk.ID != genuinelyRelevant {
		t.Fatalf("expected genuinelyRelevant (vector #1) to rank first, got %v (score: %f)",
			got[0].Chunk.ID, got[0].Score)
	}
}
