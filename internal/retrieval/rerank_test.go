package retrieval

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/go-faster/sisyphus/internal/index"
)

// fakeStatSearcher is a fakeSearcher that also reports corpus stats, so New
// wires it as the Service's TermStater.
type fakeStatSearcher struct {
	fakeSearcher
	total int
	df    map[string]int
}

func (f fakeStatSearcher) TotalDocs(context.Context) (int, error) { return f.total, nil }

func (f fakeStatSearcher) DocFreq(_ context.Context, term string) (int, error) {
	return f.df[term], nil
}

func textResult(id uuid.UUID, score float64, vector bool, text string) index.Result {
	r := result(id, score, vector, nil)
	r.Chunk.Text = text
	return r
}

func TestTokenizeQuery(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"dropshield env", []string{"dropshield", "env"}},
		{"How Tenant ENV works", []string{"how", "tenant", "env", "works"}},
		{"spec.replicas a", []string{"spec", "replicas"}}, // "a" dropped (len<2)
		{"dup dup", []string{"dup"}},                      // deduped
		{"  ", nil},
	}
	for _, tt := range tests {
		got := tokenizeQuery(tt.in)
		if len(got) == 0 && len(tt.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("tokenizeQuery(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

// TestSpecificityDemotesBroadOnlyMatch verifies the reported "dropshield env"
// case: a chunk matching only the broad term "env" is demoted below a chunk
// that matches the rare anchor "dropshield".
func TestSpecificityDemotesBroadOnlyMatch(t *testing.T) {
	anchorHit := uuid.New()
	broadOnly := uuid.New()

	// env is broad (high df), dropshield is rare (low df) => dropshield is anchor.
	lexical := fakeStatSearcher{
		fakeSearcher: fakeSearcher{results: []index.Result{
			// broadOnly ranks first lexically (matches "env" heavily) but is off-topic.
			textResult(broadOnly, 1.0, false, "environment env env config env"),
			textResult(anchorHit, 0.5, false, "the dropshield service reads env"),
		}},
		total: 1000,
		df:    map[string]int{"env": 400, "dropshield": 3},
	}

	svc, err := New(lexical, nil, nil, ServiceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.Retrieve(t.Context(), index.Query{Text: "dropshield env", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("no results")
	}
	if got[0].Chunk.ID != anchorHit {
		t.Fatalf("expected anchor-matching chunk first, got %v", got[0].Chunk.ID)
	}
	// broadOnly should be demoted (anchor penalty). It may survive or be cut by
	// minScoreFraction; either way it must not outrank the anchor hit.
	for i, r := range got {
		if r.Chunk.ID == broadOnly && i == 0 {
			t.Fatal("broad-only chunk should not rank first")
		}
	}
}

// TestSpecificitySkippedForSingleToken ensures single-token queries are not
// reranked: a vector-only semantic hit that lacks the literal term keeps its
// natural ranking rather than being penalized.
func TestSpecificitySkippedForSingleToken(t *testing.T) {
	semantic := uuid.New()

	lexical := fakeStatSearcher{
		fakeSearcher: fakeSearcher{results: []index.Result{
			textResult(semantic, 1.0, false, "unrelated words only"),
		}},
		total: 1000,
		df:    map[string]int{"dropshield": 3},
	}

	svc, err := New(lexical, nil, nil, ServiceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.Retrieve(t.Context(), index.Query{Text: "dropshield", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 result, got %d", len(got))
	}
	// Single-token: no anchor penalty applied. Score is pure RRF * authority
	// (no authority meta here), so it equals the RRF contribution 1/(60+1).
	want := 1.0 / (rrfK + 1)
	if got[0].Score != want {
		t.Fatalf("single-token score reranked: got %v, want %v (no specificity)", got[0].Score, want)
	}
}

// TestServiceUsesLexicalStats verifies New wires a stats-capable lexical backend
// as the TermStater, and a plain one leaves it nil.
func TestServiceUsesLexicalStats(t *testing.T) {
	withStats, err := New(fakeStatSearcher{total: 1}, nil, nil, ServiceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if withStats.stats == nil {
		t.Fatal("expected stats to be wired from stat-capable lexical backend")
	}

	plain, err := New(fakeSearcher{}, nil, nil, ServiceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if plain.stats != nil {
		t.Fatal("expected nil stats for non-stat lexical backend")
	}
}
