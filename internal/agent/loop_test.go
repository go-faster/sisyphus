package agent

import (
	"context"
	"encoding/json"
	"fmt"
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

func (f *fakeLLM) CompleteWithTools(ctx context.Context, model string, messages []openai.ChatCompletionMessageParamUnion, tools []openai.ChatCompletionToolUnionParam) (openai.ChatCompletionMessage, Usage, error) {
	if f.calls >= len(f.responses) {
		return openai.ChatCompletionMessage{}, Usage{}, errors.New("no more mock responses")
	}
	resp := f.responses[f.calls]
	f.calls++
	return resp, Usage{}, nil
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

func submitReportCall(id string, r Report) openai.ChatCompletionMessageToolCallUnion {
	args, err := json.Marshal(r)
	if err != nil {
		panic(err)
	}
	return toolCall(id, submitReportToolName, string(args))
}

func TestLoop_Run_HappyPath(t *testing.T) {
	llm := &fakeLLM{
		responses: []openai.ChatCompletionMessage{
			{
				Content:   "Let me check.",
				ToolCalls: []openai.ChatCompletionMessageToolCallUnion{toolCall("call_1", "test_tool", `{"foo":"bar"}`)},
			},
			{
				ToolCalls: []openai.ChatCompletionMessageToolCallUnion{
					submitReportCall("call_2", Report{Problem: "test problem", Verdict: VerdictSolved, Findings: "the result is test_tool_result"}),
				},
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
	require.Equal(t, Report{Problem: "test problem", Verdict: VerdictSolved, Findings: "the result is test_tool_result"}, res.Report)
	require.Equal(t, 2, res.Iterations)
	require.Equal(t, 1, res.ToolsUsed)
	require.Equal(t, []string{"test_tool"}, ts.calls)
}

func TestLoop_Run_MaxIterations(t *testing.T) {
	// maxIterations=2 grants a 3rd, grace attempt after the "wrap up soon"
	// warning; the mock still doesn't submit on any of the 3 calls, so the
	// loop errors out with Iterations reflecting all 3 attempts.
	llm := &fakeLLM{
		responses: []openai.ChatCompletionMessage{
			{
				Content:   "I'll try one.",
				ToolCalls: []openai.ChatCompletionMessageToolCallUnion{toolCall("call_err", "error_tool", "{}")},
			},
			{
				Content:   "I'll try again.",
				ToolCalls: []openai.ChatCompletionMessageToolCallUnion{toolCall("call_err2", "error_tool", "{}")},
			},
			{
				Content:   "I'll try once more.",
				ToolCalls: []openai.ChatCompletionMessageToolCallUnion{toolCall("call_err3", "error_tool", "{}")},
			},
		},
	}
	ts := &fakeToolSource{}

	loop := NewLoop(llm, ts, "test-model", 2, zaptest.NewLogger(t))
	res, err := loop.Run(context.Background(), "system", "user")
	require.ErrorContains(t, err, "exceeded max iterations (2)")
	require.Equal(t, 3, res.Iterations)
	require.Equal(t, 3, res.ToolsUsed)
}

func TestLoop_Run_ToolError(t *testing.T) {
	llm := &fakeLLM{
		responses: []openai.ChatCompletionMessage{
			{
				Content:   "Let me check.",
				ToolCalls: []openai.ChatCompletionMessageToolCallUnion{toolCall("call_1", "error_tool", `{}`)},
			},
			{
				ToolCalls: []openai.ChatCompletionMessageToolCallUnion{
					submitReportCall("call_2", Report{Problem: "p", Verdict: VerdictNeedsInvestigation, Findings: "the tool failed, moving on"}),
				},
			},
		},
	}
	ts := &fakeToolSource{}

	loop := NewLoop(llm, ts, "test-model", 5, zaptest.NewLogger(t))
	res, err := loop.Run(context.Background(), "system", "user")
	require.NoError(t, err)
	require.Equal(t, "the tool failed, moving on", res.Report.Findings)
	require.Equal(t, 2, res.Iterations)
	require.Equal(t, 1, res.ToolsUsed)
	require.Equal(t, []string{"error_tool"}, ts.calls)
}

func TestLoop_Run_NoToolCalls(t *testing.T) {
	llm := &fakeLLM{
		responses: []openai.ChatCompletionMessage{
			{Content: "I already know the answer."},
		},
	}
	ts := &fakeToolSource{}

	loop := NewLoop(llm, ts, "test-model", 5, zaptest.NewLogger(t))
	res, err := loop.Run(context.Background(), "system", "user")
	require.NoError(t, err)
	require.Equal(t, "I already know the answer.", res.Report.Findings)
	require.Equal(t, VerdictNeedsInvestigation, res.Report.Verdict)
	require.Equal(t, 1, res.Iterations)
	require.Equal(t, 0, res.ToolsUsed)
	require.Empty(t, ts.calls)
}

func TestLoop_Run_ActionsStrippedWhenOutOfScope(t *testing.T) {
	llm := &fakeLLM{
		responses: []openai.ChatCompletionMessage{
			{
				ToolCalls: []openai.ChatCompletionMessageToolCallUnion{
					submitReportCall("call_1", Report{
						Problem: "not ours", Verdict: VerdictOutOfScope,
						Actions: []string{"page the other team"},
					}),
				},
			},
		},
	}
	ts := &fakeToolSource{}

	loop := NewLoop(llm, ts, "test-model", 5, zaptest.NewLogger(t))
	res, err := loop.Run(context.Background(), "system", "user")
	require.NoError(t, err)
	require.Equal(t, VerdictOutOfScope, res.Report.Verdict)
	require.Empty(t, res.Report.Actions)
}

func TestLoop_Shorten(t *testing.T) {
	llm := &fakeLLM{
		responses: []openai.ChatCompletionMessage{
			{
				ToolCalls: []openai.ChatCompletionMessageToolCallUnion{
					submitReportCall("call_1", Report{Problem: "p", Verdict: VerdictSolved, Findings: "a very long finding indeed"}),
				},
			},
			{
				ToolCalls: []openai.ChatCompletionMessageToolCallUnion{
					submitReportCall("call_2", Report{Problem: "p", Verdict: VerdictSolved, Findings: "short"}),
				},
			},
		},
	}
	ts := &fakeToolSource{}

	loop := NewLoop(llm, ts, "test-model", 5, zaptest.NewLogger(t))
	res, err := loop.Run(context.Background(), "system", "user")
	require.NoError(t, err)

	shortened, err := loop.Shorten(context.Background(), res, 10)
	require.NoError(t, err)
	require.Equal(t, "short", shortened.Report.Findings)
}

// TestLoop_Shorten_RespectsIterationBudget verifies Shorten no longer
// hardcodes a 3-iteration budget for its continuation regardless of the
// operator's configured MaxIterations: with a tight budget of 1, the
// continuation must exhaust after 1 iteration (min(3, 1)), not 3.
func TestLoop_Shorten_RespectsIterationBudget(t *testing.T) {
	responses := []openai.ChatCompletionMessage{
		{
			ToolCalls: []openai.ChatCompletionMessageToolCallUnion{
				submitReportCall("call_1", Report{Problem: "p", Verdict: VerdictSolved, Findings: "a very long finding indeed"}),
			},
		},
	}
	// The model never calls submit_report again during Shorten, so the
	// continuation always exhausts its iteration budget.
	for i := range 5 {
		responses = append(responses, openai.ChatCompletionMessage{
			ToolCalls: []openai.ChatCompletionMessageToolCallUnion{toolCall(fmt.Sprintf("call_tool_%d", i), "test_tool", "{}")},
		})
	}
	llm := &fakeLLM{responses: responses}
	ts := &fakeToolSource{
		tools: []openai.ChatCompletionToolUnionParam{
			{OfFunction: &openai.ChatCompletionFunctionToolParam{Function: openai.FunctionDefinitionParam{Name: "test_tool"}}},
		},
	}

	loop := NewLoop(llm, ts, "test-model", 1, zaptest.NewLogger(t))
	res, err := loop.Run(context.Background(), "system", "user")
	require.NoError(t, err)

	_, err = loop.Shorten(context.Background(), res, 10)
	require.ErrorIs(t, err, ErrMaxIterations)
	require.EqualError(t, err, "continue loop: exceeded max iterations (1)")
}
