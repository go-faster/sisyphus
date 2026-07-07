// Package retrieval merges and reranks results from multiple Searchers
// (Postgres FTS + Qdrant vectors) and applies authority/boost rules (plan §10, §11).
package retrieval

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/index"
)

// rrfK is the constant used in Reciprocal Rank Fusion (RRF), following standard IR literature.
const rrfK = 60.0

// minScoreFraction drops results scoring below this fraction of the top
// result's score after boosting. RRF scores are not calibrated absolute
// relevance (they're reciprocal-rank sums), so a fixed threshold doesn't
// mean anything across queries; a fraction of the top score does.
const minScoreFraction = 0.2

// authorityWeight maps a source authority to a multiplicative boost (plan §11).
var authorityWeight = map[index.Authority]float64{
	index.AuthorityHigh:       1.4,
	index.AuthorityMediumHigh: 1.25,
	index.AuthorityMedium:     1.1,
	index.AuthorityLowMedium:  1.0,
	index.AuthorityLow:        0.85,
}

// ServiceOptions configures observability for the retrieval Service.
type ServiceOptions struct {
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
}

func (opts *ServiceOptions) setDefaults() {
	if opts.TracerProvider == nil {
		opts.TracerProvider = otel.GetTracerProvider()
	}
	if opts.MeterProvider == nil {
		opts.MeterProvider = otel.GetMeterProvider()
	}
}

// Service merges lexical and vector search and ranks the combined set.
type Service struct {
	lexical index.Searcher
	vector  index.Searcher
	fetcher ChunkFetcher
	stats   TermStater
	tracer  trace.Tracer
	m       *retrievalMetrics
}

// ChunkFetcher hydrates chunk fields that are intentionally not stored in
// vector payloads. Postgres remains the source of truth for chunk text.
type ChunkFetcher interface {
	FetchChunks(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]index.Chunk, error)
}

// New builds a retrieval Service. Either searcher may be nil (e.g. vector
// search unavailable); at least one must be set.
func New(lexical, vector index.Searcher, fetcher ChunkFetcher, opts ServiceOptions) (*Service, error) {
	if lexical == nil && vector == nil {
		return nil, errors.New("retrieval: at least one searcher required")
	}
	opts.setDefaults()

	m, err := newRetrievalMetrics(opts.MeterProvider)
	if err != nil {
		return nil, errors.Wrap(err, "retrieval metrics")
	}

	svc := &Service{
		lexical: lexical,
		vector:  vector,
		fetcher: fetcher,
		tracer:  opts.TracerProvider.Tracer("github.com/go-faster/sisyphus/retrieval"),
		m:       m,
	}
	// The lexical backend (Postgres FTS) can report corpus document frequencies,
	// which enable IDF-weighted specificity reranking. Optional: if the backend
	// doesn't expose stats, reranking is skipped.
	if ts, ok := lexical.(TermStater); ok {
		svc.stats = ts
	}
	return svc, nil
}

// Retrieve runs both backends, merges by chunk ID, applies boosts, and returns
// the top results sorted by final score (plan §10 steps 3-7).
func (s *Service) Retrieve(ctx context.Context, q index.Query) (_ []index.Result, rerr error) {
	start := time.Now()
	defer func() {
		s.m.searchDur.Record(ctx, time.Since(start).Seconds())
	}()

	queryLen := utf8.RuneCountInString(q.Text)
	ctx, span := s.tracer.Start(ctx, "retrieval.Retrieve",
		trace.WithAttributes(
			attribute.Int("query.length", queryLen),
			attribute.Int("query.limit", q.Limit),
			attribute.Bool("backend.lexical.enabled", s.lexical != nil),
			attribute.Bool("backend.vector.enabled", s.vector != nil),
		),
	)
	defer func() {
		if rerr != nil {
			span.RecordError(rerr)
			span.SetStatus(codes.Error, rerr.Error())
		}
		span.End()
	}()

	if q.Limit <= 0 {
		q.Limit = 30
	}

	var (
		wg            sync.WaitGroup
		lexicalResult []index.Result
		vectorResult  []index.Result
	)
	if s.lexical != nil {
		wg.Go(func() {
			lexicalResult = s.searchBackend(ctx, "lexical", s.lexical, q)
		})
	}
	if s.vector != nil {
		wg.Go(func() {
			rs := s.searchBackend(ctx, "vector", s.vector, q)
			if rs == nil {
				return
			}
			s.hydrate(ctx, rs)
			vectorResult = rs
		})
	}
	wg.Wait()
	span.AddEvent("lexical_search.done", trace.WithAttributes(attribute.Int("results.count", len(lexicalResult))))
	span.AddEvent("vector_search.done", trace.WithAttributes(attribute.Int("results.count", len(vectorResult))))

	// Merge lexical and vector results using Reciprocal Rank Fusion (RRF).
	// RRF is scale-free and does not require normalizing scores across backends
	// with different scoring ranges (unbounded ts_rank vs bounded cosine similarity).
	merged := map[string]*index.Result{}
	mergedRRFScore := map[string]float64{}

	// Helper to apply RRF to a backend's result list.
	// Results should already be sorted by score descending; we sort again defensively
	// in case a future Searcher implementation doesn't guarantee sorted order.
	applyRRF := func(rs []index.Result) {
		// Sort by score descending (defensive).
		slices.SortStableFunc(rs, func(a, b index.Result) int {
			return cmp.Compare(b.Score, a.Score) // descending
		})

		// Accumulate RRF contributions for each chunk.
		for rank, r := range rs {
			key := r.Chunk.ID.String()
			// RRF contribution: 1.0 / (k + rank+1), where rank is 0-indexed.
			contribution := 1.0 / (rrfK + float64(rank+1))

			// Store the result object once (for chunk metadata).
			if _, ok := merged[key]; !ok {
				rc := r
				merged[key] = &rc
			}
			// Accumulate RRF score.
			mergedRRFScore[key] += contribution
		}
	}

	if lexicalResult != nil {
		applyRRF(lexicalResult)
	}
	if vectorResult != nil {
		applyRRF(vectorResult)
	}

	if len(merged) == 0 {
		s.m.recordResults(ctx, 0)
		span.SetAttributes(attribute.Int("results.count", 0), attribute.Bool("results.empty", true))
		return nil, nil
	}

	// Specificity rerank: reintroduce the term-rarity signal that rank-based RRF
	// fusion discards, so a rare anchor term (e.g. "dropshield") dominates a
	// broad one (e.g. "env"). nil for single-token queries / no stats.
	spec := s.specificityBoost(ctx, q)

	out := make([]index.Result, 0, len(merged))
	for key, r := range merged {
		// Set the result's score to the RRF score, then apply authority/service/jira boosts.
		r.Score = mergedRRFScore[key]
		r.Score = boost(*r, q)
		if spec != nil {
			r.Score *= spec(*r)
		}
		out = append(out, *r)
	}
	slices.SortStableFunc(out, func(a, b index.Result) int {
		if a.Score != b.Score {
			return cmp.Compare(b.Score, a.Score) // descending
		}
		return cmp.Compare(a.Chunk.ID.String(), b.Chunk.ID.String())
	})
	if topScore := out[0].Score; topScore > 0 {
		threshold := topScore * minScoreFraction
		cut := len(out)
		for i, r := range out {
			if r.Score < threshold {
				cut = i
				break
			}
		}
		out = out[:cut]
	}
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	s.m.recordResults(ctx, len(out))
	span.SetAttributes(attribute.Int("results.count", len(out)), attribute.Bool("results.empty", false))
	return out, nil
}

func (s *Service) searchBackend(ctx context.Context, backend string, searcher index.Searcher, q index.Query) (_ []index.Result) {
	start := time.Now()
	ctx, span := s.tracer.Start(ctx, "retrieval.backend_search",
		trace.WithAttributes(attribute.String("backend", backend)),
	)
	defer span.End()

	rs, err := searcher.Search(ctx, q)
	s.m.recordBackend(ctx, backend, len(rs), time.Since(start).Seconds(), err)
	span.SetAttributes(attribute.Int("results.count", len(rs)))
	span.AddEvent("backend_search.done", trace.WithAttributes(attribute.Int("results.count", len(rs))))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		zctx.From(ctx).Warn(backend+" search failed", zap.Error(err))
		return nil
	}
	return rs
}

func (s *Service) hydrate(ctx context.Context, rs []index.Result) {
	if s.fetcher == nil {
		return
	}
	ids := make([]uuid.UUID, 0, len(rs))
	seen := map[uuid.UUID]bool{}
	for _, r := range rs {
		if r.Chunk.Text != "" || r.Chunk.ID == uuid.Nil || seen[r.Chunk.ID] {
			continue
		}
		seen[r.Chunk.ID] = true
		ids = append(ids, r.Chunk.ID)
	}
	if len(ids) == 0 {
		return
	}
	ctx, span := s.tracer.Start(ctx, "retrieval.hydrate_chunks",
		trace.WithAttributes(attribute.Int("chunks.requested", len(ids))),
	)
	defer span.End()
	chunks, err := s.fetcher.FetchChunks(ctx, ids)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		zctx.From(ctx).Warn("hydrate vector chunks failed", zap.Error(err), zap.Int("count", len(ids)))
		return
	}
	found := 0
	for i := range rs {
		if rs[i].Chunk.Text != "" {
			continue
		}
		chunk, ok := chunks[rs[i].Chunk.ID]
		if !ok {
			continue
		}
		rs[i].Chunk.Text = chunk.Text
		rs[i].Chunk.TokenCount = chunk.TokenCount
		found++
	}
	span.SetAttributes(attribute.Int("chunks.found", found))
	span.AddEvent("hydrate.done", trace.WithAttributes(
		attribute.Int("chunks.requested", len(ids)),
		attribute.Int("chunks.found", found),
	))
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

	// Exact identifier in the query text: very strong (Jira key / GitLab MR number).
	text := strings.ToLower(q.Text)
	if key, ok := meta["jira_key"].(string); ok && key != "" {
		if strings.Contains(text, strings.ToLower(key)) {
			score *= 2.0
		}
	}
	if _, ok := meta["gitlab_mr"]; ok {
		if iid, ok := meta["iid"].(int); ok && iid > 0 {
			if strings.Contains(text, fmt.Sprintf("!%d", iid)) {
				score *= 2.0
			}
		}
	}

	return score
}
