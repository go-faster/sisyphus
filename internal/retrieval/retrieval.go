// Package retrieval merges and reranks results from multiple Searchers
// (Postgres FTS + Qdrant vectors) and applies authority/boost rules (plan §10, §11).
package retrieval

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/go-faster/errors"
	"go.uber.org/zap"

	"github.com/go-faster/scpbot/internal/index"
)

// authorityWeight maps a source authority to a multiplicative boost (plan §11).
var authorityWeight = map[index.Authority]float64{
	index.AuthorityHigh:       1.4,
	index.AuthorityMediumHigh: 1.25,
	index.AuthorityMedium:     1.1,
	index.AuthorityLowMedium:  1.0,
	index.AuthorityLow:        0.85,
}

// Service merges lexical and vector search and ranks the combined set.
type Service struct {
	lexical index.Searcher
	vector  index.Searcher
	log     *zap.Logger
}

// New builds a retrieval Service. Either searcher may be nil (e.g. vector
// search unavailable); at least one must be set.
func New(lexical, vector index.Searcher, log *zap.Logger) (*Service, error) {
	if lexical == nil && vector == nil {
		return nil, errors.New("retrieval: at least one searcher required")
	}
	if log == nil {
		log = zap.NewNop()
	}
	return &Service{lexical: lexical, vector: vector, log: log}, nil
}

// Retrieve runs both backends, merges by chunk ID, applies boosts, and returns
// the top results sorted by final score (plan §10 steps 3-7).
func (s *Service) Retrieve(ctx context.Context, q index.Query) ([]index.Result, error) {
	if q.Limit <= 0 {
		q.Limit = 30
	}

	merged := map[string]*index.Result{}
	add := func(rs []index.Result) {
		for i := range rs {
			r := rs[i]
			key := r.Chunk.ID.String()
			if existing, ok := merged[key]; ok {
				// Combine lexical+vector evidence: keep the larger base score and
				// mark that it was found by both (a small co-occurrence boost).
				if r.Score > existing.Score {
					existing.Score = r.Score
				}
				existing.Score *= 1.1
				continue
			}
			rc := r
			merged[key] = &rc
		}
	}

	if s.lexical != nil {
		rs, err := s.lexical.Search(ctx, q)
		if err != nil {
			s.log.Warn("lexical search failed", zap.Error(err))
		} else {
			add(rs)
		}
	}
	if s.vector != nil {
		rs, err := s.vector.Search(ctx, q)
		if err != nil {
			s.log.Warn("vector search failed", zap.Error(err))
		} else {
			add(rs)
		}
	}
	if len(merged) == 0 {
		return nil, nil
	}

	out := make([]index.Result, 0, len(merged))
	for _, r := range merged {
		r.Score = boost(*r, q)
		out = append(out, *r)
	}
	slices.SortStableFunc(out, func(a, b index.Result) int {
		if a.Score != b.Score {
			return cmp.Compare(b.Score, a.Score) // descending
		}
		return cmp.Compare(a.Chunk.ID.String(), b.Chunk.ID.String())
	})
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// boost applies authority and exact-match boosts to a result's score (plan §11).
func boost(r index.Result, q index.Query) float64 {
	score := r.Score
	meta := r.Chunk.Metadata

	if a, ok := meta["authority"].(string); ok {
		if w, ok := authorityWeight[index.Authority(a)]; ok {
			score *= w
		}
	}

	// Exact service match: strong boost (plan §11).
	if q.Service != "" {
		if svc, ok := meta["service"].(string); ok && strings.EqualFold(svc, q.Service) {
			score *= 1.5
		}
	}

	// Exact identifier in the query text: very strong (Jira key) / strong.
	text := strings.ToLower(q.Text)
	if key, ok := meta["jira_key"].(string); ok && key != "" {
		if strings.Contains(text, strings.ToLower(key)) {
			score *= 2.0
		}
	}

	return score
}
