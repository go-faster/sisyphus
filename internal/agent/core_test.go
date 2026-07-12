package agent

import (
	"context"
	"testing"

	"github.com/go-faster/errors"
	"github.com/openai/openai-go/v3"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

func TestCollectURLs_StructuredFieldsOnly(t *testing.T) {
	dst := make(map[string]struct{})
	// A search_knowledge-shaped result: source_url is trusted, but "text" is
	// untrusted ingested chunk content and must not contribute a URL.
	collectURLs(dst, `[{"source_url":"https://example.com/doc","text":"see https://evil.invalid for details"}]`)
	require.Equal(t, map[string]struct{}{"https://example.com/doc": {}}, dst)
}

func TestCollectURLs_URLKey(t *testing.T) {
	dst := make(map[string]struct{})
	collectURLs(dst, `{"url":"https://example.com/page","body":"click https://evil.invalid now"}`)
	require.Equal(t, map[string]struct{}{"https://example.com/page": {}}, dst)
}

func TestCollectURLs_NoStructuredField(t *testing.T) {
	dst := make(map[string]struct{})
	collectURLs(dst, "raw shell output mentioning https://evil.invalid with no JSON keys")
	require.Empty(t, dst)
}

func TestCollectURLs_NonJSONErrorText(t *testing.T) {
	dst := make(map[string]struct{})
	// A tool error message is plain text, not JSON — even if it happens to
	// contain a `"url": "..."`-shaped substring, it must not be parsed out.
	collectURLs(dst, `error: request failed for "url": "https://evil.invalid"`)
	require.Empty(t, dst)
}

func TestCollectURLs_KeyLikeTextInsideStringValue(t *testing.T) {
	dst := make(map[string]struct{})
	// The "url" key only counts when it's a real JSON object key, not when
	// it merely appears as text inside another field's string value (e.g.
	// ingested/injected content escaped into a JSON string).
	collectURLs(dst, `{"source_url":"https://example.com/doc","text":"{\"url\": \"https://evil.invalid\"}"}`)
	require.Equal(t, map[string]struct{}{"https://example.com/doc": {}}, dst)
}

// echoTerminal treats any arguments starting with "valid" as a successful
// terminal call and anything else as a parse failure, so tests can control
// exactly which of several tool calls in one message "wins".
func echoTerminal() TerminalTool {
	return TerminalTool{
		Name: "submit",
		Parse: func(argsJSON string) (bool, error) {
			if argsJSON == `"invalid"` {
				return false, errors.New("bad args")
			}
			return true, nil
		},
		SuccessMsg: "ok",
		ErrMsg:     func(err error) string { return err.Error() },
	}
}

func TestCoreLoop_FirstValidTerminalCallWins(t *testing.T) {
	// One assistant message carries two terminal calls: the first parses
	// successfully, the second doesn't. The loop must stop on the first
	// success rather than spinning another iteration over a stale
	// TerminalArgs with a dangling terminalErr.
	llm := &fakeLLM{
		responses: []openai.ChatCompletionMessage{
			{
				ToolCalls: []openai.ChatCompletionMessageToolCallUnion{
					toolCall("call_1", "submit", `"valid"`),
					toolCall("call_2", "submit", `"invalid"`),
				},
			},
		},
	}
	ts := &fakeToolSource{}

	res, err := coreLoop(context.Background(), llm, ts, "test-model", nil, nil, echoTerminal(), 5, zaptest.NewLogger(t))
	require.NoError(t, err)
	require.Equal(t, `"valid"`, res.TerminalArgs)
	require.Equal(t, 1, res.Iterations)
	require.Equal(t, 1, llm.calls)
}

func TestCoreLoop_ToolCallAfterTerminalNotExecuted(t *testing.T) {
	// A regular tool call listed after a successful terminal call in the
	// same message must not run: the loop ends as soon as the terminal call
	// resolves.
	llm := &fakeLLM{
		responses: []openai.ChatCompletionMessage{
			{
				ToolCalls: []openai.ChatCompletionMessageToolCallUnion{
					toolCall("call_1", "submit", `"valid"`),
					toolCall("call_2", "test_tool", `{}`),
				},
			},
		},
	}
	ts := &fakeToolSource{}

	res, err := coreLoop(context.Background(), llm, ts, "test-model", nil, nil, echoTerminal(), 5, zaptest.NewLogger(t))
	require.NoError(t, err)
	require.Equal(t, `"valid"`, res.TerminalArgs)
	require.Equal(t, 0, res.ToolsUsed)
	require.Empty(t, ts.calls)
}

func TestCoreLoop_ToolResultsAreFenced(t *testing.T) {
	// Mid-loop tool results must be wrapped in a random-tagged delimiter
	// block, same as buildSeedMessages fences the seed context, so a
	// prompt-injection payload embedded in untrusted tool output (a fetched
	// page, shell output) can't blend in with the surrounding instructions.
	llm := &fakeLLM{
		responses: []openai.ChatCompletionMessage{
			{
				ToolCalls: []openai.ChatCompletionMessageToolCallUnion{toolCall("call_1", "test_tool", `{}`)},
			},
			{
				ToolCalls: []openai.ChatCompletionMessageToolCallUnion{toolCall("call_2", "submit", `"valid"`)},
			},
		},
	}
	ts := &fakeToolSource{}

	res, err := coreLoop(context.Background(), llm, ts, "test-model", nil, nil, echoTerminal(), 5, zaptest.NewLogger(t))
	require.NoError(t, err)

	var toolMsg *openai.ChatCompletionMessageParamUnion
	for i := range res.Conversation {
		if res.Conversation[i].OfTool != nil && res.Conversation[i].OfTool.ToolCallID == "call_1" {
			toolMsg = &res.Conversation[i]
			break
		}
	}
	require.NotNil(t, toolMsg, "expected a tool message for call_1")
	content := toolMsg.OfTool.Content.OfString.Value
	require.Regexp(t, `^<<<TOOL_RESULT_[0-9a-f]{16}>>>\ntool success\n<<<END_TOOL_RESULT_[0-9a-f]{16}>>>$`, content)
}
