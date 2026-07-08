package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-faster/errors"
	"github.com/openai/openai-go/v3"
	"go.uber.org/zap"
)

// LLM provides chat completion with tools.
type LLM interface {
	CompleteWithTools(ctx context.Context, model string, messages []openai.ChatCompletionMessageParamUnion, tools []openai.ChatCompletionToolUnionParam) (openai.ChatCompletionMessage, error)
}

// ToolSource provides MCP tools and executes them.
type ToolSource interface {
	Tools(ctx context.Context) ([]openai.ChatCompletionToolUnionParam, error)
	Call(ctx context.Context, name string, argsJSON json.RawMessage) (string, error)
}

// Result is the result of an agent loop execution.
type Result struct {
	Report     string
	Iterations int
	ToolsUsed  int
}

// Loop runs the tool-calling loop.
type Loop struct {
	llm           LLM
	toolSource    ToolSource
	model         string
	maxIterations int
	logger        *zap.Logger
}

// NewLoop creates a new agent Loop.
func NewLoop(llm LLM, toolSource ToolSource, model string, maxIterations int, logger *zap.Logger) *Loop {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Loop{
		llm:           llm,
		toolSource:    toolSource,
		model:         model,
		maxIterations: maxIterations,
		logger:        logger,
	}
}

// Run executes the agent loop until it completes or reaches max iterations.
func (l *Loop) Run(ctx context.Context, systemPrompt, userInput string) (Result, error) {
	tools, err := l.toolSource.Tools(ctx)
	if err != nil {
		return Result{}, errors.Wrap(err, "get tools")
	}

	messages := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(systemPrompt),
		openai.UserMessage(userInput),
	}

	var res Result

	for range l.maxIterations {
		res.Iterations++

		msg, err := l.llm.CompleteWithTools(ctx, l.model, messages, tools)
		if err != nil {
			return res, errors.Wrap(err, "complete with tools")
		}

		if len(msg.ToolCalls) == 0 {
			// No more tool calls, we are done.
			res.Report = msg.Content
			return res, nil
		}

		messages = append(messages, msg.ToParam())

		// Execute tools
		for _, tc := range msg.ToolCalls {
			if tc.Type != "function" {
				continue
			}
			res.ToolsUsed++

			l.logger.Debug("calling tool", zap.String("tool", tc.Function.Name), zap.String("args", tc.Function.Arguments))

			toolRes, toolErr := l.toolSource.Call(ctx, tc.Function.Name, json.RawMessage(tc.Function.Arguments))
			if toolErr != nil {
				l.logger.Warn("tool call failed", zap.String("tool", tc.Function.Name), zap.Error(toolErr))
				toolRes = fmt.Sprintf("error: %v", toolErr)
			}

			messages = append(messages, openai.ToolMessage(toolRes, tc.ID))
		}
	}

	return res, errors.Errorf("exceeded max iterations (%d)", l.maxIterations)
}
