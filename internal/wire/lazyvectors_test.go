package wire

import (
	"context"
	"testing"

	"github.com/go-faster/errors"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/index"
)

type fakeConn struct {
	upserts  int
	deletes  int
	closes   int
	upsetErr error
}

func (c *fakeConn) Upsert(context.Context, []index.Chunk, [][]float32) error {
	c.upserts++
	return c.upsetErr
}

func (c *fakeConn) Delete(context.Context, []uuid.UUID) error {
	c.deletes++
	return nil
}

func (c *fakeConn) Close() error {
	c.closes++
	return nil
}

// TestLazyVectorStoreRetriesAfterFailedDial is the whole point of the type: a
// process that starts while Qdrant is down must index correctly once it is
// back, without a restart. Deciding at startup is what produced #125.
func TestLazyVectorStoreRetriesAfterFailedDial(t *testing.T) {
	conn := &fakeConn{}
	var attempts int
	s := &lazyVectorStore{dial: func(context.Context) (vectorConn, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("qdrant is down")
		}
		return conn, nil
	}}

	// First use, Qdrant down: an error the caller can act on, not a silent skip.
	err := s.Upsert(t.Context(), nil, nil)
	require.Error(t, err)
	require.Zero(t, conn.upserts)

	// Qdrant is back. No restart, no reconfiguration.
	require.NoError(t, s.Upsert(t.Context(), nil, nil))
	require.Equal(t, 1, conn.upserts)
	require.Equal(t, 2, attempts)
}

// TestLazyVectorStoreReusesConnection pins that the happy path dials once
// rather than per call.
func TestLazyVectorStoreReusesConnection(t *testing.T) {
	conn := &fakeConn{}
	var attempts int
	s := &lazyVectorStore{dial: func(context.Context) (vectorConn, error) {
		attempts++
		return conn, nil
	}}

	for range 3 {
		require.NoError(t, s.Upsert(t.Context(), nil, nil))
	}
	require.NoError(t, s.Delete(t.Context(), nil))
	require.Equal(t, 1, attempts)
	require.Equal(t, 3, conn.upserts)
	require.Equal(t, 1, conn.deletes)
}

// TestLazyVectorStoreRedialsAfterCallFailure pins that a failed call drops the
// connection, so the next one is not made through a socket to something that
// has gone away.
func TestLazyVectorStoreRedialsAfterCallFailure(t *testing.T) {
	broken := &fakeConn{upsetErr: errors.New("connection reset")}
	healthy := &fakeConn{}

	var attempts int
	s := &lazyVectorStore{dial: func(context.Context) (vectorConn, error) {
		attempts++
		if attempts == 1 {
			return broken, nil
		}
		return healthy, nil
	}}

	require.Error(t, s.Upsert(t.Context(), nil, nil))
	require.Equal(t, 1, broken.closes, "the failed connection is closed, not leaked")

	require.NoError(t, s.Upsert(t.Context(), nil, nil))
	require.Equal(t, 2, attempts)
	require.Equal(t, 1, healthy.upserts)
}

// TestLazyVectorStoreAdoptsExistingConnection pins that the startup connection
// is reused rather than a second one being dialed for indexing.
func TestLazyVectorStoreAdoptsExistingConnection(t *testing.T) {
	conn := &fakeConn{}
	s := &lazyVectorStore{
		store: conn,
		dial: func(context.Context) (vectorConn, error) {
			t.Fatal("dialed despite already holding a connection")
			return nil, nil
		},
	}

	require.NoError(t, s.Upsert(t.Context(), nil, nil))
	require.Equal(t, 1, conn.upserts)
}

func TestLazyVectorStoreCloseIsSafeWithoutConnection(t *testing.T) {
	s := &lazyVectorStore{dial: func(context.Context) (vectorConn, error) {
		t.Fatal("Close must not dial")
		return nil, nil
	}}
	require.NoError(t, s.Close())
}
