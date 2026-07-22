// Package openrouter implements index.Summarizer and index.Answerer backed by
// the OpenRouter chat-completions API via the official openai-go SDK.
package openrouter

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/go-faster/errors"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const defaultBaseURL = "https://openrouter.ai/api/v1"

// Client wraps the openai-go SDK client pointed at OpenRouter.
type Client struct {
	oc              openai.Client
	tracer          trace.Tracer
	m               *llmMetrics
	maxRetries      int
	retryBackoff    time.Duration
	reasoningEffort string
}

// Options configures a Client.
type Options struct {
	// BaseURL overrides the API base URL (useful for tests / self-hosted).
	BaseURL string
	// HTTPClient sets the HTTP client used for requests.
	HTTPClient     *http.Client
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
	// MaxRetries is the number of extra attempts for transient upstream errors
	// that OpenRouter returns inside an HTTP 200 body (see UpstreamError). The
	// openai-go SDK only retries non-2xx responses itself, so these would
	// otherwise never be retried. Default 2.
	MaxRetries int
	// RetryBackoff is the base delay between those retries; it doubles each
	// attempt. Default 500ms.
	RetryBackoff time.Duration
	// ReasoningEffort requests OpenRouter's unified reasoning mode ("low",
	// "medium", or "high"); empty leaves the request as-is, so whether a
	// completion carries a reasoning trace is entirely up to whatever
	// provider OpenRouter happens to route the request to (see
	// internal/agent/reasoning.go, which round-trips reasoning when
	// present but can't make a provider produce it). Validated in
	// internal/config.
	ReasoningEffort string
}

func (opts *Options) setDefaults() {
	if opts.BaseURL == "" {
		opts.BaseURL = defaultBaseURL
	}
	if opts.TracerProvider == nil {
		opts.TracerProvider = otel.GetTracerProvider()
	}
	if opts.MeterProvider == nil {
		opts.MeterProvider = otel.GetMeterProvider()
	}
	if opts.MaxRetries == 0 {
		opts.MaxRetries = 2
	}
	if opts.RetryBackoff == 0 {
		opts.RetryBackoff = 500 * time.Millisecond
	}
}

// New returns a Client configured for the given API key.
func New(apiKey string, opts Options) *Client {
	opts.setDefaults()
	ropts := []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithBaseURL(opts.BaseURL),
	}
	if opts.HTTPClient != nil {
		opts := option.WithHTTPClient(opts.HTTPClient)
		ropts = append(ropts, opts)
	}
	m, _ := newLLMMetrics(opts.MeterProvider)
	return &Client{
		oc:              openai.NewClient(ropts...),
		tracer:          opts.TracerProvider.Tracer("github.com/go-faster/sisyphus/internal/llm/openrouter"),
		m:               m,
		maxRetries:      opts.MaxRetries,
		retryBackoff:    opts.RetryBackoff,
		reasoningEffort: opts.ReasoningEffort,
	}
}

// withReasoning requests OpenRouter's unified reasoning mode when
// c.reasoningEffort is set. "reasoning" is an OpenRouter extension absent
// from the OpenAI schema (same as the response-side field
// internal/agent.ExtractReasoning reads), so it goes through
// SetExtraFields rather than a typed field.
func (c *Client) withReasoning(params openai.ChatCompletionNewParams) openai.ChatCompletionNewParams {
	if c.reasoningEffort == "" {
		return params
	}
	params.SetExtraFields(map[string]any{
		"reasoning": map[string]any{"effort": c.reasoningEffort},
	})
	return params
}

// newChatCompletion sends a chat-completion request and retries transient
// upstream errors that OpenRouter returns inside an HTTP 200 body (see
// UpstreamError). Transport/non-2xx errors are already retried by the openai-go
// SDK, so those are marked permanent here to avoid stacking a second retry
// layer on top.
func (c *Client) newChatCompletion(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = c.retryBackoff
	traceOpts := traceLinkOptions(ctx)
	return backoff.Retry(ctx, func() (*openai.ChatCompletion, error) {
		resp, err := c.oc.Chat.Completions.New(ctx, params, traceOpts...)
		if err != nil {
			return nil, backoff.Permanent(err)
		}
		if uerr := upstreamError(resp.RawJSON()); uerr != nil {
			return nil, uerr
		}
		return resp, nil
	}, backoff.WithBackOff(bo), backoff.WithMaxTries(uint(c.maxRetries)+1))
}

type llmMetrics struct {
	calls  metric.Int64Counter
	dur    metric.Float64Histogram
	tokens metric.Int64Counter
}

func newLLMMetrics(mp metric.MeterProvider) (*llmMetrics, error) {
	meter := mp.Meter("github.com/go-faster/sisyphus/llm/openrouter")
	calls, err := meter.Int64Counter(
		"sisyphus.llm.calls",
		metric.WithDescription("LLM calls per operation, model, and status"),
	)
	if err != nil {
		return nil, err
	}
	dur, err := meter.Float64Histogram(
		"sisyphus.llm.duration",
		metric.WithDescription("LLM call duration"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}
	tokens, err := meter.Int64Counter(
		"sisyphus.llm.tokens",
		metric.WithDescription("LLM token usage by operation and type"),
	)
	if err != nil {
		return nil, err
	}
	return &llmMetrics{calls: calls, dur: dur, tokens: tokens}, nil
}

func (m *llmMetrics) record(ctx context.Context, op, model string, durSeconds float64, promptTokens, completionTokens int64, err error) {
	status := "ok"
	if err != nil {
		status = "error"
	}
	m.calls.Add(ctx, 1,
		metric.WithAttributes(
			attribute.String("operation", op),
			attribute.String("model", model),
			attribute.String("status", status),
		),
	)
	m.dur.Record(ctx, durSeconds,
		metric.WithAttributes(
			attribute.String("operation", op),
			attribute.String("model", model),
			attribute.String("status", status),
		),
	)
	if promptTokens > 0 {
		m.tokens.Add(ctx, promptTokens,
			metric.WithAttributes(
				attribute.String("operation", op),
				attribute.String("model", model),
				attribute.String("type", "prompt"),
			),
		)
	}
	if completionTokens > 0 {
		m.tokens.Add(ctx, completionTokens,
			metric.WithAttributes(
				attribute.String("operation", op),
				attribute.String("model", model),
				attribute.String("type", "completion"),
			),
		)
	}
}

func (c *Client) complete(ctx context.Context, model string, messages []openai.ChatCompletionMessageParamUnion) (_ string, rerr error) {
	start := time.Now()
	var promptTokens, completionTokens int64
	ctx, span := c.tracer.Start(ctx, "llm.complete",
		trace.WithAttributes(attribute.String("model", model)),
	)
	defer func() {
		if c.m != nil {
			c.m.record(ctx, "complete", model, time.Since(start).Seconds(), promptTokens, completionTokens, rerr)
		}
		if rerr != nil {
			span.RecordError(rerr)
			span.SetStatus(codes.Error, rerr.Error())
		}
		span.End()
	}()

	resp, err := c.newChatCompletion(ctx, c.withReasoning(openai.ChatCompletionNewParams{
		Model:    model,
		Messages: messages,
	}))
	if err != nil {
		return "", errors.Wrap(err, "chat completion")
	}
	if len(resp.Choices) == 0 {
		return "", errors.New("openrouter returned no choices")
	}
	content := strings.TrimSpace(resp.Choices[0].Message.Content)
	promptTokens = resp.Usage.PromptTokens
	completionTokens = resp.Usage.CompletionTokens
	span.SetAttributes(
		attribute.Int64("tokens.prompt", promptTokens),
		attribute.Int64("tokens.completion", completionTokens),
	)
	return content, nil
}
