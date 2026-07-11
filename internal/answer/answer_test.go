package answer

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/go-faster/sisyphus/internal/agent"
	"github.com/go-faster/sisyphus/internal/index"
)

type answerToolSource struct {
	tools []openai.ChatCompletionToolUnionParam
	vals  map[string]string
}

func (a *answerToolSource) Tools(ctx context.Context) ([]openai.ChatCompletionToolUnionParam, error) {
	return a.tools, nil
}

func (a *answerToolSource) Call(ctx context.Context, name string, argsJSON json.RawMessage) (string, error) {
	if v, ok := a.vals[name]; ok {
		return v, nil
	}
	return "", nil
}

var _ agent.ToolSource = (*answerToolSource)(nil)

func TestAnswer(t *testing.T) {
	llm := &fakeLLM{responses: []openai.ChatCompletionMessage{{ToolCalls: []openai.ChatCompletionMessageToolCallUnion{submitAnswerCall("call_1", "hello", []index.Link{{Text: "Doc", URL: "https://example.com/doc"}, {Text: "Nope", URL: "https://evil.invalid"}})}}}}
	ts := &answerToolSource{tools: []openai.ChatCompletionToolUnionParam{searchKnowledgeTool()}, vals: map[string]string{}}
	a := NewAgenticAnswerer(llm, ts, "test-model", AgenticOptions{Logger: zaptest.NewLogger(t), PreSearch: false})
	ans, err := a.Answer(context.Background(), index.Query{Text: "question"}, []index.Result{{Chunk: index.Chunk{Metadata: map[string]any{"source_url": "https://example.com/doc"}}}})
	require.NoError(t, err)
	require.Equal(t, "hello", ans.Text)
	require.Equal(t, []index.Link{{Text: "Doc", URL: "https://example.com/doc"}}, ans.Links)
}

type recordingRetriever struct {
	calls   int
	queries []index.Query
	results []index.Result
}

func (r *recordingRetriever) Retrieve(_ context.Context, q index.Query) ([]index.Result, error) {
	r.calls++
	r.queries = append(r.queries, q)
	return r.results, nil
}

// TestAnswer_SkipsPreSearchWhenResultsProvided ensures Answer reuses
// the caller-supplied results (which may carry service/source-tier filters
// the caller applied) instead of silently re-retrieving with a bare
// Text+Limit query that would drop those filters.
func TestAnswer_SkipsPreSearchWhenResultsProvided(t *testing.T) {
	llm := &fakeLLM{responses: []openai.ChatCompletionMessage{{ToolCalls: []openai.ChatCompletionMessageToolCallUnion{submitAnswerCall("call_1", "hello", nil)}}}}
	ts := &answerToolSource{tools: []openai.ChatCompletionToolUnionParam{searchKnowledgeTool()}}
	retr := &recordingRetriever{}
	a := NewAgenticAnswerer(llm, ts, "test-model", AgenticOptions{Logger: zaptest.NewLogger(t), PreSearch: true, Retriever: retr})

	seed := []index.Result{{Chunk: index.Chunk{Metadata: map[string]any{"source_url": "https://example.com/doc"}}}}
	_, err := a.Answer(context.Background(), index.Query{Text: "question"}, seed)
	require.NoError(t, err)
	require.Zero(t, retr.calls, "pre-search must not run when the caller already retrieved results")
}

// TestAnswer_PreSearchesWhenNoResultsProvided keeps the fallback search
// working when the caller passes an empty/nil result set.
func TestAnswer_PreSearchesWhenNoResultsProvided(t *testing.T) {
	llm := &fakeLLM{responses: []openai.ChatCompletionMessage{{ToolCalls: []openai.ChatCompletionMessageToolCallUnion{submitAnswerCall("call_1", "hello", nil)}}}}
	ts := &answerToolSource{tools: []openai.ChatCompletionToolUnionParam{searchKnowledgeTool()}}
	retr := &recordingRetriever{}
	a := NewAgenticAnswerer(llm, ts, "test-model", AgenticOptions{Logger: zaptest.NewLogger(t), PreSearch: true, Retriever: retr})

	_, err := a.Answer(context.Background(), index.Query{Text: "question", Service: "svc", Filters: map[string]string{"key": "val"}, SourceTier: "code", SourcePrefixes: []string{"git_code:repo"}}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, retr.calls)
	require.Equal(t, index.Query{Text: "question", Service: "svc", Filters: map[string]string{"key": "val"}, SourceTier: "code", SourcePrefixes: []string{"git_code:repo"}, Limit: 12}, retr.queries[0])
}

func TestAnswer_Fallback(t *testing.T) {
	llm := &fakeLLM{responses: []openai.ChatCompletionMessage{{Content: "fallback"}}}
	ts := &answerToolSource{tools: []openai.ChatCompletionToolUnionParam{searchKnowledgeTool()}}
	a := NewAgenticAnswerer(llm, ts, "test-model", AgenticOptions{Logger: zaptest.NewLogger(t), PreSearch: false})
	ans, err := a.Answer(context.Background(), index.Query{Text: "question"}, nil)
	require.NoError(t, err)
	require.Equal(t, "fallback", ans.Text)
}

// capturingLLM records the system prompt it was asked to complete with, so
// tests can assert on how AgenticAnswerer builds it.
type capturingLLM struct {
	systemPrompt string
	response     openai.ChatCompletionMessage
}

func (c *capturingLLM) CompleteWithTools(_ context.Context, _ string, messages []openai.ChatCompletionMessageParamUnion, _ []openai.ChatCompletionToolUnionParam) (openai.ChatCompletionMessage, error) {
	if len(messages) > 0 {
		if sys := messages[0].OfSystem; sys != nil && sys.Content.OfString.Valid() {
			c.systemPrompt = sys.Content.OfString.Value
		}
	}
	return c.response, nil
}

func TestAnswer_SandboxDisabledNotedInPrompt(t *testing.T) {
	llm := &capturingLLM{response: openai.ChatCompletionMessage{ToolCalls: []openai.ChatCompletionMessageToolCallUnion{submitAnswerCall("call_1", "hello", nil)}}}
	ts := &answerToolSource{tools: []openai.ChatCompletionToolUnionParam{searchKnowledgeTool()}}
	a := NewAgenticAnswerer(llm, ts, "test-model", AgenticOptions{Logger: zaptest.NewLogger(t), PreSearch: false, SandboxEnabled: false})
	_, err := a.Answer(context.Background(), index.Query{Text: "question"}, nil)
	require.NoError(t, err)
	require.Contains(t, llm.systemPrompt, "NOT available")
	require.NotContains(t, llm.systemPrompt, "The sandbox machine is named")
}

func TestAnswer_SandboxEnabledNamesMachine(t *testing.T) {
	llm := &capturingLLM{response: openai.ChatCompletionMessage{ToolCalls: []openai.ChatCompletionMessageToolCallUnion{submitAnswerCall("call_1", "hello", nil)}}}
	ts := &answerToolSource{tools: []openai.ChatCompletionToolUnionParam{searchKnowledgeTool()}}
	a := NewAgenticAnswerer(llm, ts, "test-model", AgenticOptions{Logger: zaptest.NewLogger(t), PreSearch: false, SandboxEnabled: true, SandboxMachine: "sandbox"})
	_, err := a.Answer(context.Background(), index.Query{Text: "question"}, nil)
	require.NoError(t, err)
	require.Contains(t, llm.systemPrompt, "The sandbox machine is named sandbox")
}
