package wire

import (
	"context"

	"github.com/go-faster/errors"

	"github.com/go-faster/sisyphus/internal/config"
	"github.com/go-faster/sisyphus/internal/index"
	"github.com/go-faster/sisyphus/internal/search/qdrant"
)

// NewVectorStore connects to Qdrant and ensures its collection exists.
//
// [NewServices] calls this once and degrades to no vector store when it fails,
// which is right for a process that can still serve Postgres results. It is
// exported for the callers that cannot live with a decision made once at
// startup: `ssingest maint` resolves a store per sweep, so a Qdrant outage
// costs one failed run instead of disabling maintenance until the pod restarts.
//
// The caller owns the returned store and must Close it.
func NewVectorStore(ctx context.Context, cfg config.Config, embedder index.Embedder) (*qdrant.Store, error) {
	host, port, err := splitHostPort(cfg.QdrantAddr)
	if err != nil {
		return nil, errors.Wrap(err, "qdrant addr")
	}

	store, err := qdrant.New(qdrant.Config{
		Host:       host,
		Port:       port,
		Collection: cfg.QdrantCollection,
		Dim:        cfg.EmbedDim,
		Embedder:   embedder,
	})
	if err != nil {
		return nil, errors.Wrap(err, "qdrant")
	}
	if err := store.EnsureCollection(ctx); err != nil {
		_ = store.Close()
		return nil, errors.Wrap(err, "qdrant collection")
	}
	return store, nil
}
