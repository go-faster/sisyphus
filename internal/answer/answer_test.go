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

func TestAnswerRich(t *testing.T) {
	llm := &fakeLLM{responses: []openai.ChatCompletionMessage{{ToolCalls: []openai.ChatCompletionMessageToolCallUnion{submitAnswerCall("call_1", "hello", []index.Link{{Text: "Doc", URL: "https://example.com/doc"}, {Text: "Nope", URL: "https://evil.invalid"}})}}}}
	ts := &answerToolSource{tools: []openai.ChatCompletionToolUnionParam{searchKnowledgeTool()}, vals: map[string]string{}}
	a := NewAgenticAnswerer(llm, ts, "test-model", AgenticOptions{Logger: zaptest.NewLogger(t), PreSearch: false})
	ans, err := a.AnswerRich(context.Background(), "question", []index.Result{{Chunk: index.Chunk{Metadata: map[string]any{"source_url": "https://example.com/doc"}}}})
	require.NoError(t, err)
	require.Equal(t, "hello", ans.Text)
	require.Equal(t, []index.Link{{Text: "Doc", URL: "https://example.com/doc"}}, ans.Links)
}

func TestAnswerRich_Fallback(t *testing.T) {
	llm := &fakeLLM{responses: []openai.ChatCompletionMessage{{Content: "fallback"}}}
	ts := &answerToolSource{tools: []openai.ChatCompletionToolUnionParam{searchKnowledgeTool()}}
	a := NewAgenticAnswerer(llm, ts, "test-model", AgenticOptions{Logger: zaptest.NewLogger(t), PreSearch: false})
	ans, err := a.AnswerRich(context.Background(), "question", nil)
	require.NoError(t, err)
	require.Equal(t, "fallback", ans.Text)
}
