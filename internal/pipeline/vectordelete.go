package pipeline

import (
	"context"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/go-faster/sdk/zctx"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// deleteStaleVectors removes the points of chunks that no longer exist.
//
// This runs after the Postgres transaction has committed, so a failure cannot be
// rolled back: the points are simply stranded, and with no chunk row left
// pointing at them nothing will ever find them again. That is how orphans
// accumulate, and they are not inert — a vector hit is hydrated from Postgres by
// chunk ID, so an orphan resolves to empty text and can still take a candidate
// slot. Hence the retry: a transient vector-store blip should not permanently
// leak points.
//
// Exhausting the retries stays non-fatal. The document itself is correctly
// indexed either way, and failing the run here would re-chunk and re-embed it on
// the next pass to fix nothing. What leaks is reclaimed by `ssingest gc`
// (internal/vectorgc).
func (p *Pipeline) deleteStaleVectors(ctx context.Context, ids []uuid.UUID) error {
	op := func() (struct{}, error) {
		return struct{}{}, p.vectors.Delete(ctx, ids)
	}
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = p.vectorDeleteInterval
	_, err := backoff.Retry(ctx, op,
		backoff.WithBackOff(bo),
		backoff.WithMaxTries(p.vectorDeleteTries),
		backoff.WithNotify(func(err error, d time.Duration) {
			zctx.From(ctx).Warn("retrying stale vector point delete",
				zap.Error(err),
				zap.Duration("delay", d),
				zap.Int("count", len(ids)),
			)
		}),
	)
	return err
}
