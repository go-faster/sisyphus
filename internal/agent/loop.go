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

	// engineResult carries the raw exchange that produced Report, so a
	// caller can hand the Result back to Loop.Shorten to continue it rather
	// than starting a fresh investigation.
	engineResult EngineResult[Report]
}

// Loop runs the investigate agent's tool-calling loop.
type Loop struct {
	engine *Engine[Report]
}

// NewLoop creates a new agent Loop.
func NewLoop(llm LLM, toolSource ToolSource, model string, maxIterations int, logger *zap.Logger) *Loop {
	spec := TerminalSpec[Report]{
		Name:  submitReportToolName,
		Def:   submitReportTool(),
		Parse: parseReport,
		Fallback: func(noToolContent string) Report {
			return Report{Findings: noToolContent, Verdict: VerdictNeedsInvestigation}.normalize()
		},
		SuccessMsg: "ok",
		ErrMsg: func(err error) string {
			return fmt.Sprintf("error: %v", err)
		},
	}
	return &Loop{engine: NewEngine(llm, toolSource, model, maxIterations, logger, spec)}
}

// Run executes the agent loop until it calls submit_report or reaches max
// iterations.
func (l *Loop) Run(ctx context.Context, systemPrompt, userInput string) (Result, error) {
	messages := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(systemPrompt),
		openai.UserMessage(userInput),
	}
	er, err := l.engine.Run(ctx, messages)
	if err != nil {
		return toResult(er), err
	}
	return toResult(er), nil
}

// Shorten continues a prior Result's conversation, asking the model to
// resubmit a shorter report. limitChars is the character budget to ask for.
func (l *Loop) Shorten(ctx context.Context, res Result, limitChars int) (Result, error) {
	extra := openai.UserMessage(fmt.Sprintf(
		"Your report is too long. Call %s again with a shorter version: keep only the "+
			"essential facts and stay under %d characters total across all fields.",
		submitReportToolName, limitChars,
	))
	er, err := l.engine.Continue(ctx, res.engineResult, extra, 3)
	if err != nil {
		return Result{}, errors.Wrap(err, "continue loop")
	}
	return toResult(er), nil
}

func toResult(er EngineResult[Report]) Result {
	return Result{
		Report:       er.Value,
		Iterations:   er.Iterations,
		ToolsUsed:    er.ToolsUsed,
		engineResult: er,
	}
}
