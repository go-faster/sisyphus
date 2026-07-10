package mcpclient

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/go-faster/sisyphus/internal/index"
	"github.com/go-faster/sisyphus/internal/llm/stub"
	"github.com/go-faster/sisyphus/internal/mcpserver"
)

type fakeRetriever struct {
	results []index.Result
	err     error
}

func (f fakeRetriever) Retrieve(_ context.Context, _ index.Query) ([]index.Result, error) {
	return f.results, f.err
}

func TestClient(t *testing.T) {
	ctx := t.Context()

	docID := uuid.New()
	chunkID := uuid.New()
	fake := fakeRetriever{results: []index.Result{{
		Chunk: index.Chunk{
			ID:         chunkID,
			DocumentID: docID,
			Title:      "Billing FAQ",
			Text:       "How to change plan?",
			Type:       index.ChunkSection,
		},
		Score:  0.87,
		Vector: true,
	}}}

	answerer := stub.NewAnswerer()
	srv := mcpserver.New(fake, answerer, nil)

	clientTr, serverTr := mcp.NewInMemoryTransports()

	serverSession, err := srv.Connect(ctx, serverTr, nil)
	require.NoError(t, err)
	defer serverSession.Wait()

	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	clientSession, err := mcpClient.Connect(ctx, clientTr, nil)
	require.NoError(t, err)
	defer clientSession.Close()

	metrics, _ := newMCPMetrics(noop.NewMeterProvider())
	c := &Client{
		session: clientSession,
		m:       metrics,
	}

	require.NoError(t, c.CheckHealth(ctx))

	tools, err := c.Tools(ctx)
	require.NoError(t, err)

	// we expect tools from mcpserver
	require.NotEmpty(t, tools)

	res, err := c.Call(ctx, "search_knowledge", []byte(`{"query":"change plan","limit":5}`))
	require.NoError(t, err)
	require.Contains(t, res, "How to change plan?")
}
