// Package answer implements the agentic /context workflow.
package answer

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"time"

	"github.com/go-faster/errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/agent"
	"github.com/go-faster/sisyphus/internal/index"
)

//go:embed prompts/context.md
var defaultContextPrompt string

// AgenticOptions configures an AgenticAnswerer.
type AgenticOptions struct {
	Prompt         string
	Logger         *zap.Logger
	Retriever      Retriever
	QueryLimit     int
	PreSearch      bool
	MaxIterations  int
	TimeoutSeconds int
	MaxAnswerChars int
	SandboxMachine string
	SandboxEnabled bool
	Tracer         trace.Tracer
}

func (opts *AgenticOptions) setDefaults() {
	if opts.Prompt == "" {
		opts.Prompt = strings.TrimSpace(defaultContextPrompt)
	}
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
	if opts.QueryLimit <= 0 {
		opts.QueryLimit = 12
	}
	if opts.MaxIterations <= 0 {
		opts.MaxIterations = 6
	}
	if opts.TimeoutSeconds <= 0 {
		opts.TimeoutSeconds = 180
	}
	if opts.MaxAnswerChars <= 0 {
		opts.MaxAnswerChars = 2000
	}
	if opts.SandboxMachine == "" {
		opts.SandboxMachine = "sandbox"
	}
	if opts.Tracer == nil {
		opts.Tracer = otel.GetTracerProvider().Tracer("github.com/go-faster/sisyphus/internal/answer")
	}
}

// AgenticAnswerer implements index.Answerer via an LLM tool-calling loop.
type AgenticAnswerer struct {
	loop           *ContextLoop
	model          string
	prompt         string
	logger         *zap.Logger
	preSearch      Retriever
	queryLimit     int
	timeoutSeconds int
	maxAnswerChars int
	sandboxMachine string
	sandboxEnabled bool
	tracer         trace.Tracer
}

func NewAgenticAnswerer(llm agent.LLM, toolSource agent.ToolSource, model string, opts AgenticOptions) *AgenticAnswerer {
	opts.setDefaults()
	preSearch := opts.Retriever
	if !opts.PreSearch {
		preSearch = nil
	}
	return &AgenticAnswerer{
		loop:           NewContextLoop(llm, toolSource, model, opts.MaxIterations, opts.Logger),
		model:          model,
		prompt:         opts.Prompt,
		logger:         opts.Logger,
		preSearch:      preSearch,
		queryLimit:     opts.QueryLimit,
		timeoutSeconds: opts.TimeoutSeconds,
		maxAnswerChars: opts.MaxAnswerChars,
		sandboxMachine: opts.SandboxMachine,
		sandboxEnabled: opts.SandboxEnabled,
		tracer:         opts.Tracer,
	}
}

func (a *AgenticAnswerer) Answer(ctx context.Context, q index.Query, results []index.Result) (index.Answer, error) {
	ctx, span := a.tracer.Start(ctx, "answer.AgenticAnswer",
		trace.WithAttributes(
			attribute.String("model", a.model),
			attribute.Int("results.count", len(results)),
		),
	)
	defer span.End()

	if a.timeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(a.timeoutSeconds)*time.Second)
		defer cancel()
	}

	// Only fall back to an in-process search when the caller didn't already
	// retrieve results — the caller's query carries filters (service, source
	// tier/prefixes) that a bare Text+Limit re-search here would silently drop.
	seedResults := results
	if a.preSearch != nil && len(results) == 0 {
		preQuery := index.Query{
			Text:           q.Text,
			Service:        q.Service,
			Filters:        q.Filters,
			SourceTier:     q.SourceTier,
			SourcePrefixes: q.SourcePrefixes,
			Limit:          a.queryLimit,
		}
		retrieved, err := a.preSearch.Retrieve(ctx, preQuery)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return index.Answer{}, errors.Wrap(err, "pre-search")
		}
		seedResults = retrieved
	}

	systemPrompt := strings.TrimSpace(a.prompt)
	if a.sandboxEnabled && a.sandboxMachine != "" {
		systemPrompt += fmt.Sprintf("\n\nThe sandbox machine is named %s.", a.sandboxMachine)
	} else {
		systemPrompt += "\n\nThe sandbox/SSH shell tools described above are NOT available " +
			"for this request. Do not call ssh_* tools; rely only on search_knowledge and fetch_url."
	}
	if a.maxAnswerChars > 0 {
		systemPrompt += fmt.Sprintf("\n\nKeep the final answer under %d characters.", a.maxAnswerChars)
	}
	loopRes, err := a.loop.Run(ctx, systemPrompt, q.Text, seedResults)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return index.Answer{}, err
	}
	span.SetAttributes(
		attribute.Int("answer.len", len(loopRes.Answer.Text)),
		attribute.Int("answer.links", len(loopRes.Answer.Links)),
	)
	return loopRes.Answer, nil
}

var _ index.Answerer = (*AgenticAnswerer)(nil)
