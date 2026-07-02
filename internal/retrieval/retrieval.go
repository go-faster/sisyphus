// Package retrieval merges and reranks results from multiple Searchers
// (Postgres FTS + Qdrant vectors) and applies authority/boost rules (plan §10, §11).
package retrieval

import (
	"cmp"
	"context"
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

	return &Service{
		lexical: lexical,
		vector:  vector,
		fetcher: fetcher,
		tracer:  opts.TracerProvider.Tracer("github.com/go-faster/sisyphus/retrieval"),
		m:       m,
	}, nil
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
	if lexicalResult != nil {
		add(lexicalResult)
	}
	if vectorResult != nil {
		add(vectorResult)
	}
	if len(merged) == 0 {
		s.m.recordResults(ctx, 0)
		span.SetAttributes(attribute.Int("results.count", 0), attribute.Bool("results.empty", true))
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

	// Exact identifier in the query text: very strong (Jira key) / strong.
	text := strings.ToLower(q.Text)
	if key, ok := meta["jira_key"].(string); ok && key != "" {
		if strings.Contains(text, strings.ToLower(key)) {
			score *= 2.0
		}
	}

	return score
}
