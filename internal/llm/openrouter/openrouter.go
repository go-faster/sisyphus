// Package openrouter implements index.Summarizer and index.Answerer backed by
// the OpenRouter chat-completions API via the official openai-go SDK.
package openrouter

import (
	"context"
	"net/http"
	"strings"
	"time"

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
	oc     openai.Client
	tracer trace.Tracer
	m      *llmMetrics
}

// Options configures a Client.
type Options struct {
	// BaseURL overrides the API base URL (useful for tests / self-hosted).
	BaseURL string
	// HTTPClient sets the HTTP client used for requests.
	HTTPClient     *http.Client
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
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
		oc:     openai.NewClient(ropts...),
		tracer: opts.TracerProvider.Tracer("github.com/go-faster/scpbot/llm/openrouter"),
		m:      m,
	}
}

type llmMetrics struct {
	calls  metric.Int64Counter
	dur    metric.Float64Histogram
	tokens metric.Int64Counter
}

func newLLMMetrics(mp metric.MeterProvider) (*llmMetrics, error) {
	meter := mp.Meter("github.com/go-faster/scpbot/llm/openrouter")
	calls, err := meter.Int64Counter(
		"scpbot.llm.calls",
		metric.WithDescription("LLM calls per operation, model, and status"),
	)
	if err != nil {
		return nil, err
	}
	dur, err := meter.Float64Histogram(
		"scpbot.llm.duration",
		metric.WithDescription("LLM call duration"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}
	tokens, err := meter.Int64Counter(
		"scpbot.llm.tokens",
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

	resp, err := c.oc.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:    model,
		Messages: messages,
	})
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
