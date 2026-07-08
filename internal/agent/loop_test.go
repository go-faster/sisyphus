package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/go-faster/errors"
	"github.com/openai/openai-go/v3"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

type fakeLLM struct {
	responses []openai.ChatCompletionMessage
	calls     int
}

func (f *fakeLLM) CompleteWithTools(ctx context.Context, model string, messages []openai.ChatCompletionMessageParamUnion, tools []openai.ChatCompletionToolUnionParam) (openai.ChatCompletionMessage, error) {
	if f.calls >= len(f.responses) {
		return openai.ChatCompletionMessage{}, errors.New("no more mock responses")
	}
	resp := f.responses[f.calls]
	f.calls++
	return resp, nil
}

type fakeToolSource struct {
	tools []openai.ChatCompletionToolUnionParam
	calls []string
	err   error
}

func (f *fakeToolSource) Tools(ctx context.Context) ([]openai.ChatCompletionToolUnionParam, error) {
	return f.tools, f.err
}

func (f *fakeToolSource) Call(ctx context.Context, name string, argsJSON json.RawMessage) (string, error) {
	f.calls = append(f.calls, name)
	if name == "error_tool" {
		return "", errors.New("tool error")
	}
	return "tool success", nil
}

func TestLoop_Run_HappyPath(t *testing.T) {
	llm := &fakeLLM{
		responses: []openai.ChatCompletionMessage{
			{
				Content: "Let me check.",
				ToolCalls: []openai.ChatCompletionMessageToolCallUnion{
					{
						ID:   "call_1",
						Type: "function",
						Function: openai.ChatCompletionMessageFunctionToolCallFunction{
							Name:      "test_tool",
							Arguments: `{"foo":"bar"}`,
						},
					},
				},
			},
			{
				Content: "The result is test_tool_result.",
			},
		},
	}
	ts := &fakeToolSource{
		tools: []openai.ChatCompletionToolUnionParam{
			{
				OfFunction: &openai.ChatCompletionFunctionToolParam{
					Function: openai.FunctionDefinitionParam{
						Name: "test_tool",
					},
				},
			},
		},
	}

	loop := NewLoop(llm, ts, "test-model", 5, zaptest.NewLogger(t))
	res, err := loop.Run(context.Background(), "system", "user")
	require.NoError(t, err)
	require.Equal(t, "The result is test_tool_result.", res.Report)
	require.Equal(t, 2, res.Iterations)
	require.Equal(t, 1, res.ToolsUsed)
	require.Equal(t, []string{"test_tool"}, ts.calls)
}

func TestLoop_Run_MaxIterations(t *testing.T) {
	llm := &fakeLLM{
		responses: []openai.ChatCompletionMessage{
			{
				Content: "I'll try one.",
				ToolCalls: []openai.ChatCompletionMessageToolCallUnion{
					{
						ID:   "call_err",
						Type: "function",
						Function: openai.ChatCompletionMessageFunctionToolCallFunction{
							Name:      "error_tool",
							Arguments: "{}",
						},
					},
				},
			},
			{
				Content: "I'll try again.",
				ToolCalls: []openai.ChatCompletionMessageToolCallUnion{
					{
						ID:   "call_err2",
						Type: "function",
						Function: openai.ChatCompletionMessageFunctionToolCallFunction{
							Name:      "error_tool",
							Arguments: "{}",
						},
					},
				},
			},
		},
	}
	ts := &fakeToolSource{}

	loop := NewLoop(llm, ts, "test-model", 2, zaptest.NewLogger(t))
	_, err := loop.Run(context.Background(), "system", "user")
	require.ErrorContains(t, err, "exceeded max iterations (2)")
}

func TestLoop_Run_ToolError(t *testing.T) {
	llm := &fakeLLM{
		responses: []openai.ChatCompletionMessage{
			{
				Content: "Let me check.",
				ToolCalls: []openai.ChatCompletionMessageToolCallUnion{
					{
						ID:   "call_1",
						Type: "function",
						Function: openai.ChatCompletionMessageFunctionToolCallFunction{
							Name:      "error_tool",
							Arguments: `{}`,
						},
					},
				},
			},
			{
				Content: "The tool failed, moving on.",
			},
		},
	}
	ts := &fakeToolSource{}

	loop := NewLoop(llm, ts, "test-model", 5, zaptest.NewLogger(t))
	res, err := loop.Run(context.Background(), "system", "user")
	require.NoError(t, err)
	require.Equal(t, "The tool failed, moving on.", res.Report)
	require.Equal(t, 2, res.Iterations)
	require.Equal(t, 1, res.ToolsUsed)
	require.Equal(t, []string{"error_tool"}, ts.calls)
}

func TestLoop_Run_ZeroTools(t *testing.T) {
	llm := &fakeLLM{
		responses: []openai.ChatCompletionMessage{
			{
				Content: "I already know the answer.",
			},
		},
	}
	ts := &fakeToolSource{}

	loop := NewLoop(llm, ts, "test-model", 5, zaptest.NewLogger(t))
	res, err := loop.Run(context.Background(), "system", "user")
	require.NoError(t, err)
	require.Equal(t, "I already know the answer.", res.Report)
	require.Equal(t, 1, res.Iterations)
	require.Equal(t, 0, res.ToolsUsed)
	require.Empty(t, ts.calls)
}
