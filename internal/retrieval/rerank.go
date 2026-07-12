package retrieval

import (
	"context"
	"math"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/go-faster/sdk/zctx"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/index"
)

// TermStater reports corpus document frequencies used for IDF-weighted
// specificity reranking. The Postgres searcher implements it; when the active
// lexical backend does not, specificity reranking is silently skipped.
type TermStater interface {
	// DocFreq returns the number of chunks whose full-text index matches term.
	DocFreq(ctx context.Context, term string) (int, error)
	// TotalDocs returns the total number of indexed chunks.
	TotalDocs(ctx context.Context) (int, error)
}

// anchorPenalty multiplies a candidate's score when a multi-term query has one
// or more rare "anchor" terms and the candidate contains none of them. Broad
// noise (e.g. "env"-only hits for a query like "dropshield env") is demoted
// below the minScoreFraction cut without being removed from recall.
const anchorPenalty = 0.2

// anchorFraction defines how close to the rarest term's IDF a term must be to
// count as an anchor. With 0.8, the top ~20% IDF band is treated as anchors, so
// queries with two comparably-rare terms keep both as anchors.
const anchorFraction = 0.8

// specificityBoost returns a per-candidate score multiplier that rewards
// IDF-weighted coverage of the query terms and penalizes candidates that miss
// the rare anchor term(s). It returns nil (no reranking) when:
//   - the lexical backend does not expose corpus stats,
//   - the query has a single token (recall already works; no anchor to miss), or
//   - corpus stats are unavailable at query time.
//
// This runs after RRF fusion, so it reintroduces the term-specificity signal
// that rank-based fusion discards, without replacing the fusion itself.
func (s *Service) specificityBoost(ctx context.Context, q index.Query) func(index.Result) float64 {
	if s.stats == nil {
		return nil
	}
	terms := tokenizeQuery(q.Text)
	if len(terms) <= 1 {
		// Skip all specificity logic for single-token queries (safer): coverage
		// would just punish vector-only semantic hits that don't literally
		// contain the term, and single-term recall already ranks well.
		return nil
	}

	total, err := s.stats.TotalDocs(ctx)
	if err != nil || total <= 0 {
		if err != nil {
			zctx.From(ctx).Warn("specificity: total docs failed", zap.Error(err))
		}
		return nil
	}

	// Fetch each term's doc frequency concurrently: on a cache miss this is a
	// Postgres round-trip per term, and running them sequentially means
	// latency scales with the number of query terms instead of the slowest
	// one.
	dfs := make([]int, len(terms))
	errs := make([]error, len(terms))
	var wg sync.WaitGroup
	for i, t := range terms {
		wg.Add(1)
		go func(i int, t string) {
			defer wg.Done()
			dfs[i], errs[i] = s.stats.DocFreq(ctx, t)
		}(i, t)
	}
	wg.Wait()

	idf := make(map[string]float64, len(terms))
	var maxIDF float64
	for i, t := range terms {
		if err := errs[i]; err != nil {
			zctx.From(ctx).Warn("specificity: doc freq failed",
				zap.String("term", t), zap.Error(err))
			return nil
		}
		// idf = ln(1 + N/(df+1)): monotonically decreasing in df, with rare
		// terms (small df) weighted highest and df=0 giving the max weight.
		w := math.Log1p(float64(total) / float64(dfs[i]+1))
		idf[t] = w
		maxIDF = math.Max(maxIDF, w)
	}

	var totalIDF float64
	for _, w := range idf {
		totalIDF += w
	}
	if totalIDF <= 0 {
		return nil
	}

	// Anchor terms are those within anchorFraction of the rarest term's IDF.
	anchors := make(map[string]bool, len(idf))
	for t, w := range idf {
		if w >= maxIDF*anchorFraction {
			anchors[t] = true
		}
	}

	return func(r index.Result) float64 {
		hay := candidateHaystack(r)
		var matchedIDF float64
		anchorHit := false
		for t, w := range idf {
			if strings.Contains(hay, t) {
				matchedIDF += w
				if anchors[t] {
					anchorHit = true
				}
			}
		}
		boost := 1.0 + matchedIDF/totalIDF
		if len(anchors) > 0 && !anchorHit {
			boost *= anchorPenalty
		}
		return boost
	}
}

// candidateHaystack builds the lowercased text a query term is matched against.
// Title and structured identifier fields (symbol/name/path/kind) are included
// so an anchor appearing in a symbol name or file path counts as a match, not
// only body text.
func candidateHaystack(r index.Result) string {
	var b strings.Builder
	b.WriteString(r.Chunk.Title)
	b.WriteByte(' ')
	b.WriteString(r.Chunk.Text)
	for _, k := range []string{"symbol", "name", "path", "kind"} {
		if v, ok := r.Chunk.Metadata[k].(string); ok && v != "" {
			b.WriteByte(' ')
			b.WriteString(v)
		}
	}
	return strings.ToLower(b.String())
}

// tokenizeQuery splits a query into lowercased, deduped tokens of length >= 2,
// approximating the 'simple' FTS tokenization used by the Postgres backend.
func tokenizeQuery(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	seen := make(map[string]bool, len(fields))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.ToLower(f)
		if utf8.RuneCountInString(f) < 2 {
			continue
		}
		if seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}
