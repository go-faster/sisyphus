package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/go-faster/errors"
	"github.com/openai/openai-go/v3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// TerminalTool describes the submit tool that ends the loop.
type TerminalTool struct {
	Name       string
	Def        openai.ChatCompletionToolUnionParam
	Parse      func(argsJSON string) (terminal bool, err error)
	SuccessMsg string
	ErrMsg     func(err error) string
}

// CoreResult is the exported form of coreResult for other packages that share
// the loop engine.
type CoreResult = coreResult

// coreResult holds the loop's raw output for the caller to interpret.
type coreResult struct {
	Iterations     int
	ToolsUsed      int
	Conversation   []openai.ChatCompletionMessageParamUnion
	Tools          []openai.ChatCompletionToolUnionParam
	TerminalArgs   string
	NoToolContent  string
	DiscoveredURLs map[string]struct{}
}

// coreLoop runs the generic LLM ↔ tool-calling loop until the terminal tool
// is called or maxIterations is reached.
func coreLoop(ctx context.Context, llm LLM, toolSource ToolSource, model string,
	messages []openai.ChatCompletionMessageParamUnion,
	tools []openai.ChatCompletionToolUnionParam,
	terminal TerminalTool,
	maxIterations int,
	logger *zap.Logger,
) (coreResult, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	span := trace.SpanFromContext(ctx)

	var res coreResult
	res.Tools = tools
	res.DiscoveredURLs = make(map[string]struct{})

	// Every tool result is untrusted content (ingested chunks, fetched pages,
	// raw shell output) fed back to the model as a ToolMessage. buildSeedMessages
	// (internal/answer/framing.go) fences the *seed* search results with a random
	// delimiter tag so the model can visually distinguish data from instructions;
	// mid-loop results need the same treatment since they're the more likely
	// vector for a prompt-injection payload (arbitrary fetched/shell content, not
	// curated seed chunks). One tag is generated per loop run and reused for every
	// tool result in the conversation.
	tag, err := randomTag()
	if err != nil {
		return res, errors.Wrap(err, "generate delimiter tag")
	}

	for range maxIterations {
		res.Iterations++
		span.AddEvent("agent.iteration", trace.WithAttributes(attribute.Int("iteration", res.Iterations)))

		msg, err := llm.CompleteWithTools(ctx, model, messages, tools)
		if err != nil {
			return res, errors.Wrap(err, "complete with tools")
		}
		messages = append(messages, msg.ToParam())

		if len(msg.ToolCalls) == 0 {
			res.NoToolContent = msg.Content
			res.Conversation = messages
			span.AddEvent("agent." + terminal.Name)
			return res, nil
		}

		// First valid terminal call wins and ends the loop immediately: any
		// further tool calls in the same message (terminal or not) are left
		// unexecuted rather than risk an incoherent state (e.g. a second,
		// unparseable terminal call overwriting a captured TerminalArgs).
		var done bool
		for _, tc := range msg.ToolCalls {
			if tc.Type != "function" {
				continue
			}

			if tc.Function.Name == terminal.Name {
				called := true
				if terminal.Parse != nil {
					var err error
					called, err = terminal.Parse(tc.Function.Arguments)
					if err != nil {
						messages = append(messages, openai.ToolMessage(terminal.ErrMsg(err), tc.ID))
						continue
					}
				}
				if !called {
					continue
				}
				res.TerminalArgs = tc.Function.Arguments
				messages = append(messages, openai.ToolMessage(terminal.SuccessMsg, tc.ID))
				done = true
				break
			}

			res.ToolsUsed++
			logger.Debug("calling tool", zap.String("tool", tc.Function.Name), zap.String("args", tc.Function.Arguments))
			span.AddEvent("agent.tool_call", trace.WithAttributes(attribute.String("tool", tc.Function.Name)))

			toolRes, toolErr := toolSource.Call(ctx, tc.Function.Name, json.RawMessage(tc.Function.Arguments))
			if toolErr != nil {
				logger.Warn("tool call failed", zap.String("tool", tc.Function.Name), zap.Error(toolErr))
				toolRes = fmt.Sprintf("error: %v", toolErr)
			}
			collectURLs(res.DiscoveredURLs, toolRes)
			messages = append(messages, openai.ToolMessage(fenceToolResult(tag, toolRes), tc.ID))
		}

		if done {
			res.Conversation = messages
			span.AddEvent("agent." + terminal.Name)
			return res, nil
		}
	}

	return res, errors.Errorf("exceeded max iterations (%d)", maxIterations)
}

// randomTag generates a short random hex tag used to delimit untrusted
// content blocks, so the delimiter itself can't be guessed/spoofed by
// injected content in a tool result.
func randomTag() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// fenceToolResult wraps a tool result in a random-tagged delimiter block so
// the model can visually distinguish untrusted tool output from its own
// instructions, mirroring buildSeedMessages' fencing of seed search results.
func fenceToolResult(tag, text string) string {
	return fmt.Sprintf("<<<TOOL_RESULT_%s>>>\n%s\n<<<END_TOOL_RESULT_%s>>>", tag, text, tag)
}

// collectURLs extracts URLs only from structured "source_url"/"url" JSON
// fields in a tool result, never from free-form body text (e.g. a chunk's
// text or a fetched page's body) — those are untrusted content and must not
// be treated as vetted source links (see filterButtons).
//
// It does this by actually decoding the result as JSON and walking the
// resulting value tree, rather than regexing the raw string: a regex over
// raw text would also match a "url": "..." substring that merely appears
// inside untrusted string content (a tool error message, ingested chunk
// text, or raw shell output from the ssh sandbox tools), letting a
// prompt-injected payload smuggle an attacker-controlled URL into the
// allowed set. json.Unmarshal only exposes object keys that are structurally
// keys, not text that happens to look like one inside a string value, and a
// non-JSON result (e.g. plain-text tool errors or shell output) simply
// yields no URLs instead of a false match.
func collectURLs(dst map[string]struct{}, text string) {
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		return
	}
	walkURLs(dst, v)
}

func walkURLs(dst map[string]struct{}, v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if k == "source_url" || k == "url" {
				if s, ok := val.(string); ok {
					addURL(dst, s)
					continue
				}
			}
			walkURLs(dst, val)
		}
	case []any:
		for _, e := range t {
			walkURLs(dst, e)
		}
	}
}

func addURL(dst map[string]struct{}, raw string) {
	raw = strings.TrimRight(strings.TrimSpace(raw), ".,;:!?)]}>")
	if raw == "" {
		return
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return
	}
	dst[raw] = struct{}{}
}

// CoreLoop is the exported entry point for the shared agent loop engine.
func CoreLoop(ctx context.Context, llm LLM, toolSource ToolSource, model string,
	messages []openai.ChatCompletionMessageParamUnion,
	tools []openai.ChatCompletionToolUnionParam,
	terminal TerminalTool,
	maxIterations int,
	logger *zap.Logger,
) (CoreResult, error) {
	return coreLoop(ctx, llm, toolSource, model, messages, tools, terminal, maxIterations, logger)
}
