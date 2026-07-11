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
	coreRes, err := coreLoop(ctx, l.llm, l.toolSource, l.model, messages, tools, TerminalTool{
		Name: submitReportToolName,
		Def:  submitReportTool(),
		Parse: func(argsJSON string) (bool, error) {
			_, err := parseReport(argsJSON)
			if err != nil {
				return false, err
			}
			return true, nil
		},
		SuccessMsg: "ok",
		ErrMsg: func(err error) string {
			return fmt.Sprintf("error: %v", err)
		},
	}, maxIterations, l.logger)
	if err != nil {
		return Result{Iterations: coreRes.Iterations, ToolsUsed: coreRes.ToolsUsed}, err
	}

	res := Result{
		Iterations:   coreRes.Iterations,
		ToolsUsed:    coreRes.ToolsUsed,
		conversation: coreRes.Conversation,
		tools:        coreRes.Tools,
	}
	if coreRes.TerminalArgs != "" {
		report, err := parseReport(coreRes.TerminalArgs)
		if err != nil {
			return Result{}, errors.Wrap(err, "parse report")
		}
		res.Report = report
		return res, nil
	}

	res.Report = Report{Findings: coreRes.NoToolContent, Verdict: VerdictNeedsInvestigation}.normalize()
	return res, nil
}
