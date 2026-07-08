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

// InvestigatorImpl implements the Investigator interface.
type InvestigatorImpl struct {
	loop *Loop
}

// NewInvestigator creates a new Investigator.
func NewInvestigator(llm LLM, toolSource ToolSource, model string, maxIterations int, logger *zap.Logger) *InvestigatorImpl {
	return &InvestigatorImpl{
		loop: NewLoop(llm, toolSource, model, maxIterations, logger),
	}
}

// Investigate performs the investigation given a symptom/issue description.
func (i *InvestigatorImpl) Investigate(ctx context.Context, description string) (Result, error) {
	return i.loop.Run(ctx, investigatePrompt, description)
}
