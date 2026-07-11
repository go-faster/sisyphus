package answer

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/go-faster/errors"
	"github.com/openai/openai-go/v3"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/go-faster/sisyphus/internal/index"
)

type fakeLLM struct {
	responses []openai.ChatCompletionMessage
	calls     int
	captured  [][]openai.ChatCompletionMessageParamUnion
}

func (f *fakeLLM) CompleteWithTools(ctx context.Context, model string, messages []openai.ChatCompletionMessageParamUnion, tools []openai.ChatCompletionToolUnionParam) (openai.ChatCompletionMessage, error) {
	f.captured = append(f.captured, append([]openai.ChatCompletionMessageParamUnion(nil), messages...))
	if f.calls >= len(f.responses) {
		return openai.ChatCompletionMessage{}, errors.New("no more mock responses")
	}
	resp := f.responses[f.calls]
	f.calls++
	return resp, nil
}

type fakeToolSource struct {
	tools map[string]openai.ChatCompletionToolUnionParam
	calls []string
	vals  map[string]string
	err   map[string]error
}

func (f *fakeToolSource) Tools(ctx context.Context) ([]openai.ChatCompletionToolUnionParam, error) {
	tools := make([]openai.ChatCompletionToolUnionParam, 0, len(f.tools))
	for _, tool := range f.tools {
		tools = append(tools, tool)
	}
	return tools, nil
}

func (f *fakeToolSource) Call(ctx context.Context, name string, argsJSON json.RawMessage) (string, error) {
	f.calls = append(f.calls, name)
	if err, ok := f.err[name]; ok {
		return "", err
	}
	if v, ok := f.vals[name]; ok {
		return v, nil
	}
	return "tool success", nil
}

func toolCall(id, name, args string) openai.ChatCompletionMessageToolCallUnion {
	return openai.ChatCompletionMessageToolCallUnion{
		ID:   id,
		Type: "function",
		Function: openai.ChatCompletionMessageFunctionToolCallFunction{
			Name:      name,
			Arguments: args,
		},
	}
}

func submitAnswerCall(id, answer string, buttons []index.Link) openai.ChatCompletionMessageToolCallUnion {
	args, err := json.Marshal(submitAnswerArgs{Answer: answer, Buttons: buttons})
	if err != nil {
		panic(err)
	}
	return toolCall(id, submitAnswerToolName, string(args))
}

func TestContextLoop_HappyPath(t *testing.T) {
	toolSource := &fakeToolSource{
		tools: map[string]openai.ChatCompletionToolUnionParam{
			"search_knowledge": searchKnowledgeTool(),
		},
		vals: map[string]string{
			"search_knowledge": `[{"chunk_id":"c1","document_id":"d1","source":"git_docs:repo","source_url":"https://example.com/doc","title":"Doc","chunk_type":"section","text":"found it","score":1,"vector":true}]`,
		},
	}
	llm := &fakeLLM{responses: []openai.ChatCompletionMessage{
		{ToolCalls: []openai.ChatCompletionMessageToolCallUnion{toolCall("call_1", "search_knowledge", `{"query":"where is it?","limit":1}`)}},
		{ToolCalls: []openai.ChatCompletionMessageToolCallUnion{submitAnswerCall("call_2", "answer text", []index.Link{{Text: "Doc", URL: "https://example.com/doc"}})}},
	}}
	loop := NewContextLoop(llm, toolSource, "test-model", 5, zaptest.NewLogger(t))
	res, err := loop.Run(context.Background(), "system prompt", "question", nil)
	require.NoError(t, err)
	require.Equal(t, "answer text", res.Answer.Text)
	require.Equal(t, []string{"search_knowledge"}, toolSource.calls)
	require.Contains(t, res.DiscoveredURLs, "https://example.com/doc")
}

func TestContextLoop_MaxIterations(t *testing.T) {
	toolSource := &fakeToolSource{tools: map[string]openai.ChatCompletionToolUnionParam{"search_knowledge": searchKnowledgeTool()}}
	llm := &fakeLLM{responses: []openai.ChatCompletionMessage{
		{ToolCalls: []openai.ChatCompletionMessageToolCallUnion{toolCall("call_1", "search_knowledge", `{}`)}},
		{ToolCalls: []openai.ChatCompletionMessageToolCallUnion{toolCall("call_2", "search_knowledge", `{}`)}},
	}}
	loop := NewContextLoop(llm, toolSource, "test-model", 2, zaptest.NewLogger(t))
	_, err := loop.Run(context.Background(), "system", "question", nil)
	require.ErrorContains(t, err, "exceeded max iterations (2)")
}

func TestContextLoop_ToolError(t *testing.T) {
	toolSource := &fakeToolSource{
		tools: map[string]openai.ChatCompletionToolUnionParam{"search_knowledge": searchKnowledgeTool()},
		err:   map[string]error{"search_knowledge": errors.New("boom")},
	}
	llm := &fakeLLM{responses: []openai.ChatCompletionMessage{
		{ToolCalls: []openai.ChatCompletionMessageToolCallUnion{toolCall("call_1", "search_knowledge", `{}`)}},
		{ToolCalls: []openai.ChatCompletionMessageToolCallUnion{submitAnswerCall("call_2", "done", nil)}},
	}}
	loop := NewContextLoop(llm, toolSource, "test-model", 5, zaptest.NewLogger(t))
	res, err := loop.Run(context.Background(), "system", "question", nil)
	require.NoError(t, err)
	require.Equal(t, "done", res.Answer.Text)
	require.Equal(t, []string{"search_knowledge"}, toolSource.calls)
}

func TestContextLoop_NoToolCalls(t *testing.T) {
	toolSource := &fakeToolSource{tools: map[string]openai.ChatCompletionToolUnionParam{"search_knowledge": searchKnowledgeTool()}}
	llm := &fakeLLM{responses: []openai.ChatCompletionMessage{{Content: "plain prose"}}}
	loop := NewContextLoop(llm, toolSource, "test-model", 5, zaptest.NewLogger(t))
	res, err := loop.Run(context.Background(), "system", "question", nil)
	require.NoError(t, err)
	require.Equal(t, "plain prose", res.Answer.Text)
}

func TestContextLoop_SeedResultsFramed(t *testing.T) {
	toolSource := &fakeToolSource{tools: map[string]openai.ChatCompletionToolUnionParam{"search_knowledge": searchKnowledgeTool()}}
	llm := &fakeLLM{responses: []openai.ChatCompletionMessage{{Content: "plain prose"}}}
	loop := NewContextLoop(llm, toolSource, "test-model", 5, zaptest.NewLogger(t))
	_, err := loop.Run(context.Background(), "system", "question", []index.Result{{Chunk: index.Chunk{Title: "Doc", Text: "body", Metadata: map[string]any{"source_url": "https://example.com/doc"}}}})
	require.NoError(t, err)
	require.NotEmpty(t, llm.captured)
	userMsg, err := json.Marshal(llm.captured[0][1])
	require.NoError(t, err)
	require.Contains(t, string(userMsg), "CONTEXT_")
	require.Contains(t, string(userMsg), "Doc")
	require.Contains(t, string(userMsg), "https://example.com/doc")
	require.Contains(t, string(userMsg), "END_CONTEXT_")
}
