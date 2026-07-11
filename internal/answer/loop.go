package answer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/go-faster/errors"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/agent"
	"github.com/go-faster/sisyphus/internal/index"
)

// ContextLoop runs the agentic /context loop using agent.coreLoop.
type ContextLoop struct {
	llm           agent.LLM
	toolSource    agent.ToolSource
	model         string
	maxIterations int
	logger        *zap.Logger
}

func NewContextLoop(llm agent.LLM, toolSource agent.ToolSource, model string, maxIterations int, logger *zap.Logger) *ContextLoop {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ContextLoop{llm: llm, toolSource: toolSource, model: model, maxIterations: maxIterations, logger: logger}
}

type ContextResult struct {
	Answer         index.Answer
	Iterations     int
	ToolsUsed      int
	DiscoveredURLs map[string]struct{}
}

func (l *ContextLoop) Run(ctx context.Context, systemPrompt, userInput string, seedResults []index.Result) (ContextResult, error) {
	tools, err := l.toolSource.Tools(ctx)
	if err != nil {
		return ContextResult{}, errors.Wrap(err, "get tools")
	}
	tools = append(tools, submitAnswerTool())

	messages, seedURLs, err := buildSeedMessages(systemPrompt, userInput, seedResults)
	if err != nil {
		return ContextResult{}, err
	}
	coreRes, err := agent.CoreLoop(ctx, l.llm, l.toolSource, l.model, messages, tools, agent.TerminalTool{
		Name: submitAnswerToolName,
		Def:  submitAnswerTool(),
		Parse: func(argsJSON string) (bool, error) {
			var args submitAnswerArgs
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return false, errors.Wrap(err, "unmarshal submit answer")
			}
			return true, nil
		},
		SuccessMsg: "ok",
		ErrMsg: func(err error) string {
			return fmt.Sprintf("error: %v", err)
		},
	}, l.maxIterations, l.logger)
	if err != nil {
		return ContextResult{Iterations: coreRes.Iterations, ToolsUsed: coreRes.ToolsUsed, DiscoveredURLs: coreRes.DiscoveredURLs}, err
	}

	urls := make(map[string]struct{}, len(seedURLs)+len(coreRes.DiscoveredURLs))
	for u := range seedURLs {
		urls[u] = struct{}{}
	}
	for u := range coreRes.DiscoveredURLs {
		urls[u] = struct{}{}
	}

	res := ContextResult{
		Iterations:     coreRes.Iterations,
		ToolsUsed:      coreRes.ToolsUsed,
		DiscoveredURLs: urls,
	}
	if coreRes.TerminalArgs == "" {
		res.Answer.Text = strings.TrimSpace(coreRes.NoToolContent)
		return res, nil
	}
	ans, err := parseSubmitAnswer(coreRes.TerminalArgs)
	if err != nil {
		return ContextResult{}, err
	}
	res.Answer = index.Answer{
		Text:  ans.Text,
		Links: filterButtons(ans.Links, urls),
	}
	return res, nil
}
