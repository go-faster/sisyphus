package answer

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/agent"
	"github.com/go-faster/sisyphus/internal/index"
)

// ContextLoop runs the agentic /context (question) loop using agent.Engine.
type ContextLoop struct {
	engine *agent.Engine[index.Answer]
}

func NewContextLoop(llm agent.LLM, toolSource agent.ToolSource, model string, maxIterations int, logger *zap.Logger) *ContextLoop {
	spec := agent.TerminalSpec[index.Answer]{
		Name:  submitAnswerToolName,
		Def:   submitAnswerTool(),
		Parse: parseSubmitAnswer,
		Fallback: func(noToolContent string) index.Answer {
			return index.Answer{Text: strings.TrimSpace(noToolContent)}
		},
		SuccessMsg: "ok",
		ErrMsg: func(err error) string {
			return fmt.Sprintf("error: %v", err)
		},
	}
	return &ContextLoop{engine: agent.NewEngine(llm, toolSource, model, maxIterations, logger, spec)}
}

type ContextResult struct {
	Answer           index.Answer
	Iterations       int
	ToolsUsed        int
	DiscoveredURLs   map[string]struct{}
	TraceID          string
	DurationMS       int64
	PromptTokens     int64
	CompletionTokens int64
}

func (l *ContextLoop) Run(ctx context.Context, systemPrompt, userInput string, seedResults []index.Result) (ContextResult, error) {
	messages, seedURLs, err := buildSeedMessages(systemPrompt, userInput, seedResults)
	if err != nil {
		return ContextResult{}, err
	}

	er, err := l.engine.Run(ctx, messages)
	if err != nil {
		return ContextResult{
			Iterations:       er.Iterations,
			ToolsUsed:        er.ToolsUsed,
			DiscoveredURLs:   er.DiscoveredURLs,
			TraceID:          er.TraceID,
			DurationMS:       er.DurationMS,
			PromptTokens:     er.PromptTokens,
			CompletionTokens: er.CompletionTokens,
		}, err
	}

	urls := make(map[string]struct{}, len(seedURLs)+len(er.DiscoveredURLs))
	for u := range seedURLs {
		urls[u] = struct{}{}
	}
	for u := range er.DiscoveredURLs {
		urls[u] = struct{}{}
	}

	return ContextResult{
		Iterations:       er.Iterations,
		ToolsUsed:        er.ToolsUsed,
		DiscoveredURLs:   urls,
		TraceID:          er.TraceID,
		DurationMS:       er.DurationMS,
		PromptTokens:     er.PromptTokens,
		CompletionTokens: er.CompletionTokens,
		Answer: index.Answer{
			Text:  er.Value.Text,
			Links: filterButtons(er.Value.Links, urls),
		},
	}, nil
}
