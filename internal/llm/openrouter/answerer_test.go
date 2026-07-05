package openrouter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/index"
)

// captureCompletion returns a fake completion server that captures the request payload.
func captureCompletion(t *testing.T, content string) (*httptest.Server, *[]map[string]any) { //nolint:gocritic // named results here would shadow the local `captured` slice
	t.Helper()
	var captured []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") == "" {
			t.Error("missing Authorization header")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var params map[string]any
		if err := json.Unmarshal(body, &params); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		captured = append(captured, params)
		resp := openai.ChatCompletion{
			Choices: []openai.ChatCompletionChoice{
				{Message: openai.ChatCompletionMessage{Content: content}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv, &captured
}

// TestAnswerer_RandomDelimiterTag verifies that different Answer() calls generate different random tags.
func TestAnswerer_RandomDelimiterTag(t *testing.T) {
	srv, captured := captureCompletion(t, "Answer 1")
	a := NewAnswerer(newClient(t, srv), "test-model", AnswererOptions{})

	results := []index.Result{
		{Chunk: index.Chunk{Title: "Doc", Text: "Some content"}},
	}

	// First call
	_, err := a.Answer(context.Background(), "Question 1", results)
	require.NoError(t, err)
	require.NotEmpty(t, *captured)

	// Extract the user message content from first request
	messages1, ok := (*captured)[0]["messages"].([]any)
	require.True(t, ok)
	require.GreaterOrEqual(t, len(messages1), 2)
	msg1Map, ok := messages1[1].(map[string]any)
	require.True(t, ok)
	content1, ok := msg1Map["content"].(string)
	require.True(t, ok)

	// Second call
	_, err = a.Answer(context.Background(), "Question 2", results)
	require.NoError(t, err)
	require.Len(t, *captured, 2)

	// Extract the user message content from second request
	messages2, ok := (*captured)[1]["messages"].([]any)
	require.True(t, ok)
	require.GreaterOrEqual(t, len(messages2), 2)
	msg2Map, ok := messages2[1].(map[string]any)
	require.True(t, ok)
	content2, ok := msg2Map["content"].(string)
	require.True(t, ok)

	// User messages should be different due to different random tags
	require.NotEqual(t, content1, content2, "each Answer() call should generate a different tag")

	// Both should contain the tag pattern
	tagPattern := regexp.MustCompile(`<<<CONTEXT_([0-9a-f]+)>>>`)
	match1 := tagPattern.FindStringSubmatch(content1)
	match2 := tagPattern.FindStringSubmatch(content2)
	require.Len(t, match1, 2)
	require.Len(t, match2, 2)
	require.NotEqual(t, match1[1], match2[1], "random tags should differ")
}

// TestAnswerer_ContextIsTagged verifies that the context is properly framed with random delimiter tags.
func TestAnswerer_ContextIsTagged(t *testing.T) {
	srv, captured := captureCompletion(t, "Test answer")
	a := NewAnswerer(newClient(t, srv), "test-model", AnswererOptions{})

	results := []index.Result{
		{Chunk: index.Chunk{Title: "Issue #123", Text: "Some potentially malicious content here"}},
		{Chunk: index.Chunk{Title: "MR #456", Text: "More untrusted data"}},
	}

	_, err := a.Answer(context.Background(), "What is the status?", results)
	require.NoError(t, err)
	require.NotEmpty(t, *captured)

	// Extract the user message content
	messages, ok := (*captured)[0]["messages"].([]any)
	require.True(t, ok)
	require.GreaterOrEqual(t, len(messages), 2)
	msgMap, ok := messages[1].(map[string]any)
	require.True(t, ok)
	content, ok := msgMap["content"].(string)
	require.True(t, ok)

	// Verify the tag structure
	tagPattern := regexp.MustCompile(`<<<CONTEXT_([0-9a-f]+)>>>`)
	matches := tagPattern.FindAllStringSubmatch(content, -1)
	require.Len(t, matches, 2, "should have opening and closing tags")
	require.Equal(t, matches[0][1], matches[1][1], "opening and closing tags should use the same random value")

	// Verify the content block is properly wrapped
	tag := matches[0][1]
	openTag := "<<<CONTEXT_" + tag + ">>>"
	closeTag := "<<<END_CONTEXT_" + tag + ">>>"

	require.Contains(t, content, openTag)
	require.Contains(t, content, closeTag)

	// Verify that the opening tag appears before the closing tag
	openIdx := strings.Index(content, openTag)
	closeIdx := strings.Index(content, closeTag)
	require.True(t, openIdx < closeIdx, "opening tag should come before closing tag")

	// Verify the context text is between the tags
	between := content[openIdx+len(openTag) : closeIdx]
	require.Contains(t, between, "Issue #123")
	require.Contains(t, between, "MR #456")
	require.Contains(t, between, "Some potentially malicious content here")
}

// TestAnswerer_SystemPromptMentionsUntrustedContext verifies the system prompt instructs about untrusted data.
func TestAnswerer_SystemPromptMentionsUntrustedContext(t *testing.T) {
	srv, captured := captureCompletion(t, "Answer")
	a := NewAnswerer(newClient(t, srv), "test-model", AnswererOptions{})

	results := []index.Result{
		{Chunk: index.Chunk{Title: "Doc", Text: "Content"}},
	}

	_, err := a.Answer(context.Background(), "Question", results)
	require.NoError(t, err)
	require.NotEmpty(t, *captured)

	// Extract the system message content
	messages, ok := (*captured)[0]["messages"].([]any)
	require.True(t, ok)
	require.GreaterOrEqual(t, len(messages), 1)
	sysMsgMap, ok := messages[0].(map[string]any)
	require.True(t, ok)
	sysContent, ok := sysMsgMap["content"].(string)
	require.True(t, ok)

	// Check that the system prompt contains security guidance
	require.Contains(t, sysContent, "CRITICAL")
	require.Contains(t, sysContent, "untrusted")
	require.Contains(t, sysContent, "DATA")
	require.Contains(t, sysContent, "instructions")
}

// TestAnswerer_SourceLabelsPreserved verifies that per-chunk source labels are preserved inside the context block.
func TestAnswerer_SourceLabelsPreserved(t *testing.T) {
	srv, captured := captureCompletion(t, "Answer")
	a := NewAnswerer(newClient(t, srv), "test-model", AnswererOptions{})

	results := []index.Result{
		{Chunk: index.Chunk{Title: "Issue #10", Text: "First chunk"}},
		{Chunk: index.Chunk{Title: "MR #20", Text: "Second chunk"}},
	}

	_, err := a.Answer(context.Background(), "Question", results)
	require.NoError(t, err)
	require.NotEmpty(t, *captured)

	// Extract the user message content
	messages, ok := (*captured)[0]["messages"].([]any)
	require.True(t, ok)
	require.GreaterOrEqual(t, len(messages), 2)
	msgMap, ok := messages[1].(map[string]any)
	require.True(t, ok)
	content, ok := msgMap["content"].(string)
	require.True(t, ok)

	// Extract content between tags
	tagPattern := regexp.MustCompile(`<<<CONTEXT_([0-9a-f]+)>>>`)
	matches := tagPattern.FindAllStringSubmatch(content, -1)
	require.Len(t, matches, 2)

	tag := matches[0][1]
	openTag := "<<<CONTEXT_" + tag + ">>>"
	closeTag := "<<<END_CONTEXT_" + tag + ">>>"

	openIdx := strings.Index(content, openTag)
	closeIdx := strings.Index(content, closeTag)
	between := content[openIdx+len(openTag) : closeIdx]

	// Verify source labels are present as informational markers
	require.Contains(t, between, "--- Source 1: Issue #10 ---")
	require.Contains(t, between, "--- Source 2: MR #20 ---")
	require.Contains(t, between, "First chunk")
	require.Contains(t, between, "Second chunk")
}
