// Package agent implements an LLM tool-calling investigation loop over MCP tools.
package agent

import (
	"context"
	_ "embed"

	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/index"
)

//go:embed prompts/investigate.md
var investigatePrompt string

// Investigator runs on-demand investigations using tools.
type Investigator interface {
	Investigate(ctx context.Context, description string) (Result, error)
}

// defaultMaxReportChars is used when no explicit limit is configured.
const defaultMaxReportChars = 1500

// InvestigatorImpl implements the Investigator interface.
type InvestigatorImpl struct {
	loop           *Loop
	maxReportChars int
	showDebugInfo  bool
	logger         *zap.Logger
}

// InvestigatorOptions configures a new InvestigatorImpl.
type InvestigatorOptions struct {
	// MaxIterations bounds the tool-calling loop's iteration budget (plus
	// one grace attempt — see coreLoop).
	MaxIterations int
	// MaxReportChars is the character budget the report is asked to fit in
	// before being handed back to the caller for delivery; a non-positive
	// value uses defaultMaxReportChars.
	MaxReportChars int
	// ShowDebugInfo attaches loop diagnostics (trace ID, duration, tool
	// calls, token usage) to the returned Report.Debug. Off by default.
	ShowDebugInfo bool
	Logger        *zap.Logger
}

func (opts *InvestigatorOptions) setDefaults() {
	if opts.MaxReportChars <= 0 {
		opts.MaxReportChars = defaultMaxReportChars
	}
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
}

// NewInvestigator creates a new Investigator.
func NewInvestigator(llm LLM, toolSource ToolSource, model string, opts InvestigatorOptions) *InvestigatorImpl {
	opts.setDefaults()
	return &InvestigatorImpl{
		loop:           NewLoop(llm, toolSource, model, opts.MaxIterations, opts.Logger),
		maxReportChars: opts.MaxReportChars,
		showDebugInfo:  opts.ShowDebugInfo,
		logger:         opts.Logger,
	}
}

// Investigate performs the investigation given a symptom/issue description.
// If the resulting report is over the configured character budget, it asks
// the model once to resubmit a shorter version before returning.
func (i *InvestigatorImpl) Investigate(ctx context.Context, description string) (Result, error) {
	res, err := i.loop.Run(ctx, investigatePrompt, description)
	if err != nil {
		return res, err
	}

	if n := res.Report.CharLen(); n > i.maxReportChars {
		shortened, shortenErr := i.loop.Shorten(ctx, res, i.maxReportChars)
		if shortenErr != nil {
			i.logger.Warn("shorten report failed, keeping original", zap.Error(shortenErr))
			return i.withDebug(res), nil
		}
		shortened.Iterations += res.Iterations
		shortened.ToolsUsed += res.ToolsUsed
		return i.withDebug(shortened), nil
	}

	return i.withDebug(res), nil
}

// withDebug attaches Report.Debug from res's engine result when
// ShowDebugInfo is enabled; res is returned unchanged otherwise.
func (i *InvestigatorImpl) withDebug(res Result) Result {
	if !i.showDebugInfo {
		return res
	}
	res.Report.Debug = &index.Debug{
		TraceID:          res.engineResult.TraceID,
		DurationMS:       res.engineResult.DurationMS,
		Iterations:       res.Iterations,
		ToolCalls:        res.ToolsUsed,
		PromptTokens:     res.engineResult.PromptTokens,
		CompletionTokens: res.engineResult.CompletionTokens,
	}
	return res
}
