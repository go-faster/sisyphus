package mcpserver

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/go-faster/sisyphus/internal/index"
	"github.com/go-faster/sisyphus/internal/llm/stub"
)

type fakeRetriever struct {
	results []index.Result
	err     error
}

func (f fakeRetriever) Retrieve(_ context.Context, _ index.Query) ([]index.Result, error) {
	return f.results, f.err
}

func TestMCPServer_ToolListAndSearch(t *testing.T) {
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
			Metadata: map[string]any{
				"source":     "gitlab_docs",
				"source_url": "https://example.com/docs/billing",
			},
		},
		Score:  0.87,
		Vector: true,
	}}}

	answerer := stub.NewAnswerer()
	srv := New(fake, answerer, nil)

	// In-memory client/server pair.
	clientTr, serverTr := mcp.NewInMemoryTransports()

	serverSession, err := srv.Connect(ctx, serverTr, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer serverSession.Wait()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	clientSession, err := client.Connect(ctx, clientTr, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer clientSession.Close()

	// List tools.
	toolsRes, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	toolNames := map[string]bool{}
	for _, tl := range toolsRes.Tools {
		toolNames[tl.Name] = true
	}
	if !toolNames["search_knowledge"] {
		t.Fatalf("missing search_knowledge tool; got %v", toolNames)
	}
	if !toolNames["answer_question"] {
		t.Fatalf("missing answer_question tool; got %v", toolNames)
	}

	// Call search_knowledge.
	callRes, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "search_knowledge",
		Arguments: map[string]any{
			"query": "change plan",
			"limit": 5,
		},
	})
	if err != nil {
		t.Fatalf("call search: %v", err)
	}
	if callRes.IsError {
		t.Fatalf("search returned error: %+v", callRes)
	}
	if len(callRes.Content) == 0 {
		t.Fatalf("expected content in result")
	}
	// Verify mapping: at least one text content should contain our chunk text or title.
	found := false
	for _, c := range callRes.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			if tc.Text != "" {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatalf("no text content in search result; got %+v", callRes.Content)
	}

	// Call answer_question (exercises answerer path).
	callRes, err = clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "answer_question",
		Arguments: map[string]any{
			"question": "change plan",
		},
	})
	if err != nil {
		t.Fatalf("call answer: %v", err)
	}
	if callRes.IsError {
		t.Fatalf("answer returned error: %+v", callRes)
	}
	if len(callRes.Content) == 0 {
		t.Fatalf("expected content in answer result")
	}
	found = false
	for _, c := range callRes.Content {
		if tc, ok := c.(*mcp.TextContent); ok && tc.Text != "" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no text content in answer result; got %+v", callRes.Content)
	}
}
