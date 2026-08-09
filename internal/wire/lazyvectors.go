package wire

import (
	"context"
	"sync"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/config"
	"github.com/go-faster/sisyphus/internal/index"
	"github.com/go-faster/sisyphus/internal/search/qdrant"
)

// lazyVectorStore is the indexing-side vector store, connected on first use and
// reconnected after a failure.
//
// It exists because the alternative — deciding at startup whether a vector store
// exists — silently corrupts the index. [NewServices] degrades to a nil store
// when Qdrant is unreachable, and pipeline.Index's embed step is guarded on that
// store being non-nil, so a process that started during a Qdrant outage wrote
// chunk rows with no vectors and no error. The document-level skip then found
// those documents unchanged on every later run, so they stayed unembedded
// forever: present in Postgres FTS, invisible to vector search, until their body
// changed or someone ran --reset.
//
// A nil store must therefore never reach the pipeline. With this, "Qdrant is
// down" surfaces where it can be handled — as an error from Upsert, which
// Index returns *before* persisting anything, so the document is retried whole
// and indexes correctly once Qdrant is back, with no restart.
//
// Search is deliberately not routed through this. A query with no vector store
// degrades to Postgres FTS and still answers; an *index* with no vector store
// silently produces documents nobody can find.
type lazyVectorStore struct {
	// dial opens a connection. It is a field so tests can inject one without
	// standing up a Qdrant.
	dial func(context.Context) (vectorConn, error)
	addr string

	mu    sync.Mutex
	store vectorConn
}

// vectorConn is a connected vector store: what pipeline.VectorStore needs, plus
// the Close that lets a failed connection be dropped and redialed.
type vectorConn interface {
	Upsert(ctx context.Context, chunks []index.Chunk, vectors [][]float32) error
	Delete(ctx context.Context, ids []uuid.UUID) error
	Close() error
}

// newLazyVectorStore builds the indexing store. store is the connection the
// caller already made, if any, so the common path where Qdrant is up costs one
// connection rather than two; nil just means the first use dials.
func newLazyVectorStore(cfg config.Config, embedder index.Embedder, store *qdrant.Store) *lazyVectorStore {
	s := &lazyVectorStore{
		addr: cfg.QdrantAddr,
		dial: func(ctx context.Context) (vectorConn, error) {
			return NewVectorStore(ctx, cfg, embedder)
		},
	}
	// A typed nil in an interface is not nil, so only adopt a real connection.
	if store != nil {
		s.store = store
	}
	return s
}

// resolve returns a connected store, dialing if this is the first use or if the
// previous attempt failed. A failed dial is not cached, so the next call retries.
func (s *lazyVectorStore) resolve(ctx context.Context) (vectorConn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.store != nil {
		return s.store, nil
	}
	store, err := s.dial(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "connect vector store")
	}
	zctx.From(ctx).Info("vector store connected", zap.String("addr", s.addr))
	s.store = store
	return store, nil
}

func (s *lazyVectorStore) Upsert(ctx context.Context, chunks []index.Chunk, vectors [][]float32) error {
	store, err := s.resolve(ctx)
	if err != nil {
		return err
	}
	if err := s.reconnectOnFailure(ctx, store.Upsert(ctx, chunks, vectors)); err != nil {
		return errors.Wrap(err, "upsert points")
	}
	return nil
}

func (s *lazyVectorStore) Delete(ctx context.Context, ids []uuid.UUID) error {
	store, err := s.resolve(ctx)
	if err != nil {
		return err
	}
	if err := s.reconnectOnFailure(ctx, store.Delete(ctx, ids)); err != nil {
		return errors.Wrap(err, "delete points")
	}
	return nil
}

// reconnectOnFailure drops the cached store when a call fails, so the next one
// dials again rather than reusing a connection to something that has gone away.
// The gRPC client reconnects on its own for most faults; this covers the rest,
// and costs one dial per failure.
func (s *lazyVectorStore) reconnectOnFailure(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	s.mu.Lock()
	if s.store != nil {
		if cerr := s.store.Close(); cerr != nil {
			zctx.From(ctx).Debug("closing failed vector store", zap.Error(cerr))
		}
		s.store = nil
	}
	s.mu.Unlock()
	return err
}

// Close releases the connection, if one was ever made.
func (s *lazyVectorStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store == nil {
		return nil
	}
	err := s.store.Close()
	s.store = nil
	return err
}
