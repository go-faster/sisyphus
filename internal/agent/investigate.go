// Package agent implements an LLM tool-calling investigation loop over MCP tools.
package agent

import (
	"context"
	_ "embed"

	"go.uber.org/zap"
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
	logger         *zap.Logger
}

// NewInvestigator creates a new Investigator. maxReportChars is the character
// budget the report is asked to fit in before being handed back to the
// caller for delivery; a non-positive value uses defaultMaxReportChars.
func NewInvestigator(llm LLM, toolSource ToolSource, model string, maxIterations, maxReportChars int, logger *zap.Logger) *InvestigatorImpl {
	if logger == nil {
		logger = zap.NewNop()
	}
	if maxReportChars <= 0 {
		maxReportChars = defaultMaxReportChars
	}
	return &InvestigatorImpl{
		loop:           NewLoop(llm, toolSource, model, maxIterations, logger),
		maxReportChars: maxReportChars,
		logger:         logger,
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
			return res, nil
		}
		shortened.Iterations += res.Iterations
		shortened.ToolsUsed += res.ToolsUsed
		return shortened, nil
	}

	return res, nil
}
