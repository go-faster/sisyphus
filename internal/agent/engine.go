package agent

import (
	"context"
	"fmt"

	"github.com/go-faster/errors"
	"github.com/openai/openai-go/v3"
	"go.uber.org/zap"
)

// TerminalSpec configures an Engine's terminal tool: how it's defined, how
// its arguments decode into T, and what to fall back to when the model ends
// the loop without calling it.
type TerminalSpec[T any] struct {
	Name       string
	Def        openai.ChatCompletionToolUnionParam
	Parse      func(argsJSON string) (T, error)
	Fallback   func(noToolContent string) T
	SuccessMsg string
	ErrMsg     func(err error) string
}

// EngineResult is an Engine.Run/Continue result.
type EngineResult[T any] struct {
	Value          T
	Iterations     int
	ToolsUsed      int
	DiscoveredURLs map[string]struct{}

	// conversation and tools carry the exchange that produced Value, so a
	// caller can hand the result back to Engine.Continue rather than
	// starting a fresh loop.
	conversation []openai.ChatCompletionMessageParamUnion
	tools        []openai.ChatCompletionToolUnionParam
}

// Engine is the generic form of the tool-calling loop shared by every agent
// (investigate's Loop, /context's ContextLoop, ...): the loop mechanics
// (coreLoop) are identical across agents, only the terminal tool's shape (T)
// and the seed messages differ.
type Engine[T any] struct {
	llm           LLM
	toolSource    ToolSource
	model         string
	maxIterations int
	logger        *zap.Logger
	spec          TerminalSpec[T]
}

// NewEngine creates a new Engine.
func NewEngine[T any](llm LLM, toolSource ToolSource, model string, maxIterations int, logger *zap.Logger, spec TerminalSpec[T]) *Engine[T] {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Engine[T]{
		llm:           llm,
		toolSource:    toolSource,
		model:         model,
		maxIterations: maxIterations,
		logger:        logger,
		spec:          spec,
	}
}

// Run fetches tools from the toolSource, appends the terminal tool, and
// executes the loop starting from messages.
func (e *Engine[T]) Run(ctx context.Context, messages []openai.ChatCompletionMessageParamUnion) (EngineResult[T], error) {
	tools, err := e.toolSource.Tools(ctx)
	if err != nil {
		return EngineResult[T]{}, errors.Wrap(err, "get tools")
	}
	tools = append(tools, e.spec.Def)
	return e.run(ctx, messages, tools, e.maxIterations)
}

// Continue resumes a prior EngineResult's conversation with an extra user
// message, reusing its tools rather than re-fetching them.
func (e *Engine[T]) Continue(ctx context.Context, prev EngineResult[T], extra openai.ChatCompletionMessageParamUnion, maxIterations int) (EngineResult[T], error) {
	if prev.conversation == nil {
		return EngineResult[T]{}, errors.New("result has no conversation to continue")
	}
	messages := append(append([]openai.ChatCompletionMessageParamUnion{}, prev.conversation...), extra)
	return e.run(ctx, messages, prev.tools, maxIterations)
}

func (e *Engine[T]) run(ctx context.Context, messages []openai.ChatCompletionMessageParamUnion, tools []openai.ChatCompletionToolUnionParam, maxIterations int) (EngineResult[T], error) {
	errMsg := e.spec.ErrMsg
	if errMsg == nil {
		errMsg = func(err error) string { return fmt.Sprintf("error: %v", err) }
	}

	coreRes, err := coreLoop(ctx, e.llm, e.toolSource, e.model, messages, tools, TerminalTool{
		Name: e.spec.Name,
		Def:  e.spec.Def,
		Parse: func(argsJSON string) (bool, error) {
			_, err := e.spec.Parse(argsJSON)
			return err == nil, err
		},
		SuccessMsg: e.spec.SuccessMsg,
		ErrMsg:     errMsg,
	}, maxIterations, e.logger)
	if err != nil {
		return EngineResult[T]{Iterations: coreRes.Iterations, ToolsUsed: coreRes.ToolsUsed, DiscoveredURLs: coreRes.DiscoveredURLs}, err
	}

	res := EngineResult[T]{
		Iterations:     coreRes.Iterations,
		ToolsUsed:      coreRes.ToolsUsed,
		DiscoveredURLs: coreRes.DiscoveredURLs,
		conversation:   coreRes.Conversation,
		tools:          coreRes.Tools,
	}
	if coreRes.TerminalArgs != "" {
		val, err := e.spec.Parse(coreRes.TerminalArgs)
		if err != nil {
			return EngineResult[T]{}, errors.Wrap(err, "parse terminal args")
		}
		res.Value = val
		return res, nil
	}
	res.Value = e.spec.Fallback(coreRes.NoToolContent)
	return res, nil
}
