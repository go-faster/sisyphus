package postgres

import (
	"context"
	"time"

	"github.com/go-faster/errors"
)

// statsTTL bounds how long cached corpus counts are reused. The corpus only
// changes during ingest (a separate process), so short-lived staleness is fine
// and it keeps IDF lookups off the hot query path.
const statsTTL = 5 * time.Minute

// dfEntry is a cached document-frequency count with its capture time.
type dfEntry struct {
	n  int
	at time.Time
}

// DocFreq returns the number of chunks whose FTS vector matches term. It is used
// for IDF-weighted specificity reranking; results are cached for statsTTL.
func (s *Searcher) DocFreq(ctx context.Context, term string) (int, error) {
	s.statsMu.Lock()
	if e, ok := s.dfCache[term]; ok && time.Since(e.at) < statsTTL {
		s.statsMu.Unlock()
		return e.n, nil
	}
	s.statsMu.Unlock()

	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM chunks WHERE search_vector @@ plainto_tsquery('simple', $1)`,
		term).Scan(&n)
	if err != nil {
		return 0, errors.Wrap(err, "count doc freq")
	}

	s.statsMu.Lock()
	s.dfCache[term] = dfEntry{n: n, at: time.Now()}
	s.statsMu.Unlock()
	return n, nil
}

// TotalDocs returns the total number of indexed chunks, cached for statsTTL.
func (s *Searcher) TotalDocs(ctx context.Context) (int, error) {
	s.statsMu.Lock()
	if !s.totalAt.IsZero() && time.Since(s.totalAt) < statsTTL {
		n := s.totalN
		s.statsMu.Unlock()
		return n, nil
	}
	s.statsMu.Unlock()

	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM chunks`).Scan(&n)
	if err != nil {
		return 0, errors.Wrap(err, "count chunks")
	}

	s.statsMu.Lock()
	s.totalN = n
	s.totalAt = time.Now()
	s.statsMu.Unlock()
	return n, nil
}
