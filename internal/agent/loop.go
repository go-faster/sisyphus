package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-faster/errors"
	"github.com/openai/openai-go/v3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
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
	Report     Report
	Iterations int
	ToolsUsed  int

	// conversation and tools carry the exchange that produced Report, so a
	// caller can hand the Result back to Loop.Shorten to continue it rather
	// than starting a fresh investigation.
	conversation []openai.ChatCompletionMessageParamUnion
	tools        []openai.ChatCompletionToolUnionParam
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

// Run executes the agent loop until it calls submit_report or reaches max
// iterations.
func (l *Loop) Run(ctx context.Context, systemPrompt, userInput string) (Result, error) {
	tools, err := l.toolSource.Tools(ctx)
	if err != nil {
		return Result{}, errors.Wrap(err, "get tools")
	}
	tools = append(tools, submitReportTool())

	messages := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(systemPrompt),
		openai.UserMessage(userInput),
	}

	return l.run(ctx, messages, tools, l.maxIterations)
}

// Shorten continues a prior Result's conversation, asking the model to
// resubmit a shorter report. limitChars is the character budget to ask for.
func (l *Loop) Shorten(ctx context.Context, res Result, limitChars int) (Result, error) {
	if res.conversation == nil {
		return Result{}, errors.New("result has no conversation to continue")
	}
	messages := append(append([]openai.ChatCompletionMessageParamUnion{}, res.conversation...), openai.UserMessage(fmt.Sprintf(
		"Your report is too long. Call %s again with a shorter version: keep only the "+
			"essential facts and stay under %d characters total across all fields.",
		submitReportToolName, limitChars,
	)))
	return l.run(ctx, messages, res.tools, 3)
}

func (l *Loop) run(ctx context.Context, messages []openai.ChatCompletionMessageParamUnion, tools []openai.ChatCompletionToolUnionParam, maxIterations int) (Result, error) {
	span := trace.SpanFromContext(ctx)

	var res Result
	res.tools = tools

	for range maxIterations {
		res.Iterations++
		span.AddEvent("agent.iteration", trace.WithAttributes(attribute.Int("iteration", res.Iterations)))

		msg, err := l.llm.CompleteWithTools(ctx, l.model, messages, tools)
		if err != nil {
			return res, errors.Wrap(err, "complete with tools")
		}
		messages = append(messages, msg.ToParam())

		if len(msg.ToolCalls) == 0 {
			// Model didn't call submit_report; fall back to treating its
			// prose as findings rather than losing the investigation.
			res.Report = Report{Findings: msg.Content, Verdict: VerdictNeedsInvestigation}.normalize()
			res.conversation = messages
			span.AddEvent("agent.submit_report", trace.WithAttributes(attribute.String("verdict", string(res.Report.Verdict))))
			return res, nil
		}

		var (
			done      bool
			reportErr error
		)
		for _, tc := range msg.ToolCalls {
			if tc.Type != "function" {
				continue
			}

			if tc.Function.Name == submitReportToolName {
				report, err := parseReport(tc.Function.Arguments)
				if err != nil {
					reportErr = err
					messages = append(messages, openai.ToolMessage(fmt.Sprintf("error: %v", err), tc.ID))
					continue
				}
				res.Report = report
				messages = append(messages, openai.ToolMessage("ok", tc.ID))
				done = true
				continue
			}

			res.ToolsUsed++
			l.logger.Debug("calling tool", zap.String("tool", tc.Function.Name), zap.String("args", tc.Function.Arguments))
			span.AddEvent("agent.tool_call", trace.WithAttributes(attribute.String("tool", tc.Function.Name)))

			toolRes, toolErr := l.toolSource.Call(ctx, tc.Function.Name, json.RawMessage(tc.Function.Arguments))
			if toolErr != nil {
				l.logger.Warn("tool call failed", zap.String("tool", tc.Function.Name), zap.Error(toolErr))
				toolRes = fmt.Sprintf("error: %v", toolErr)
			}
			messages = append(messages, openai.ToolMessage(toolRes, tc.ID))
		}

		if done && reportErr == nil {
			res.conversation = messages
			span.AddEvent("agent.submit_report", trace.WithAttributes(attribute.String("verdict", string(res.Report.Verdict))))
			return res, nil
		}
	}

	return res, errors.Errorf("exceeded max iterations (%d)", maxIterations)
}
