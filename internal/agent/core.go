package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/go-faster/errors"
	"github.com/openai/openai-go/v3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// sourceURLPattern extracts URLs only from structured "source_url"/"url" JSON
// fields in tool results, never from free-form body text (e.g. a chunk's
// text or a fetched page's body) — those are untrusted content and must not
// be treated as vetted source links (see filterButtons).
var sourceURLPattern = regexp.MustCompile(`"(?:source_url|url)"\s*:\s*"(https?://[^"\\]+)"`)

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

		var (
			done        bool
			terminalErr error
		)
		for _, tc := range msg.ToolCalls {
			if tc.Type != "function" {
				continue
			}

			if tc.Function.Name == terminal.Name {
				if terminal.Parse != nil {
					called, err := terminal.Parse(tc.Function.Arguments)
					if err != nil {
						terminalErr = err
						messages = append(messages, openai.ToolMessage(terminal.ErrMsg(err), tc.ID))
						continue
					}
					done = called
				} else {
					done = true
				}
				if done {
					terminalErr = nil
				}
				if done {
					res.TerminalArgs = tc.Function.Arguments
					messages = append(messages, openai.ToolMessage(terminal.SuccessMsg, tc.ID))
				}
				continue
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
			messages = append(messages, openai.ToolMessage(toolRes, tc.ID))
		}

		if done && terminalErr == nil {
			res.Conversation = messages
			span.AddEvent("agent." + terminal.Name)
			return res, nil
		}
	}

	return res, errors.Errorf("exceeded max iterations (%d)", maxIterations)
}

func collectURLs(dst map[string]struct{}, text string) {
	for _, m := range sourceURLPattern.FindAllStringSubmatch(text, -1) {
		raw := strings.TrimRight(m[1], ".,;:!?)]}>")
		if raw == "" {
			continue
		}
		dst[raw] = struct{}{}
	}
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
