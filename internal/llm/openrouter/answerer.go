package openrouter

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/go-faster/errors"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/go-faster/sisyphus/internal/index"
)

//go:embed prompts/answerer.md
var defaultAnswererPrompt string

// maxAnswerButtons caps how many source-link buttons an answer may carry.
const maxAnswerButtons = 6

// Answerer implements index.Answerer via OpenRouter.
type Answerer struct {
	client *Client
	model  string
	prompt string
}

// AnswererOptions configures an Answerer.
type AnswererOptions struct {
	// Prompt overrides the default system prompt.
	Prompt string
}

func (opts *AnswererOptions) setDefaults() {
	if opts.Prompt == "" {
		opts.Prompt = strings.TrimSpace(defaultAnswererPrompt)
	}
}

// NewAnswerer returns an Answerer that uses the given model.
func NewAnswerer(client *Client, model string, opts AnswererOptions) *Answerer {
	opts.setDefaults()
	return &Answerer{
		client: client,
		model:  model,
		prompt: opts.Prompt,
	}
}

// Answer constructs a grounded answer from retrieved context chunks. It asks
// the model to reply via the submit_answer tool; if the model instead answers
// in prose (some models skip tool calls), its content is returned with no
// links rather than failing.
func (a *Answerer) Answer(ctx context.Context, q index.Query, results []index.Result) (index.Answer, error) {
	ctx, span := a.client.tracer.Start(ctx, "llm.Answer",
		trace.WithAttributes(
			attribute.String("model", a.model),
			attribute.Int("results.count", len(results)),
		),
	)
	defer span.End()

	msgs, allowedURLs, err := a.buildMessages(q.Text, results)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return index.Answer{}, err
	}
	msgs = append(msgs, openai.UserMessage(
		"Reply by calling the submit_answer tool exactly once. Put the prose answer in `answer`, "+
			"using only Telegram-safe Markdown: paragraphs, short bullet or numbered lists, bold, italic, code, and inline links. "+
			"Do not use Markdown tables; rewrite tabular data as concise bullets or a labeled list. "+
			"and in `buttons` include only the sources you actually relied on, using their exact URLs "+
			"from the context above. Omit `buttons` entirely if no source has a useful URL.",
	))

	msg, err := a.client.CompleteWithTools(ctx, a.model, msgs, []openai.ChatCompletionToolUnionParam{submitAnswerTool()})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return index.Answer{}, errors.Wrap(err, "answer")
	}

	ans := parseSubmitAnswer(msg, allowedURLs)
	span.SetAttributes(
		attribute.Int("answer.len", len(ans.Text)),
		attribute.Int("answer.links", len(ans.Links)),
	)
	return ans, nil
}

// buildMessages assembles the system + user messages framing the retrieved
// context, and returns the set of source URLs present in that context so
// model-produced button URLs can be validated against real sources.
func (a *Answerer) buildMessages(question string, results []index.Result) ([]openai.ChatCompletionMessageParamUnion, map[string]struct{}, error) {
	// Generate a random delimiter tag to prevent prompt injection via retrieved content.
	// This tag frames the context block so that injected content cannot forge context boundaries.
	// Prompt framing only; no tool/action surface reachable from this function.
	var tagBytes [8]byte
	if _, err := rand.Read(tagBytes[:]); err != nil {
		return nil, nil, errors.Wrap(err, "generate delimiter tag")
	}
	tag := hex.EncodeToString(tagBytes[:])

	allowedURLs := make(map[string]struct{})
	var sb strings.Builder
	for i, r := range results {
		fmt.Fprintf(&sb, "--- Source %d", i+1)
		if r.Chunk.Title != "" {
			fmt.Fprintf(&sb, ": %s", r.Chunk.Title)
		}
		if u := metaString(r.Chunk.Metadata, "source_url"); u != "" {
			fmt.Fprintf(&sb, " <%s>", u)
			allowedURLs[u] = struct{}{}
		}
		fmt.Fprintf(&sb, " ---\n%s\n\n", r.Chunk.Text)
	}

	contextBlock := fmt.Sprintf("<<<CONTEXT_%s>>>\n%s<<<END_CONTEXT_%s>>>", tag, sb.String(), tag)
	msgs := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(a.prompt),
		openai.UserMessage(fmt.Sprintf("Untrusted context (between <<<CONTEXT_%s>>> markers):\n%s\n\nQuestion: %s", tag, contextBlock, question)),
	}
	return msgs, allowedURLs, nil
}

const submitAnswerToolName = "submit_answer"

// submitAnswerTool forces the model to return a structured answer: prose plus
// optional source-link buttons, so the caller can render links reliably.
func submitAnswerTool() openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionToolUnionParam{
		OfFunction: &openai.ChatCompletionFunctionToolParam{
			Function: openai.FunctionDefinitionParam{
				Name:        submitAnswerToolName,
				Description: openai.String("Return the final answer. Call this exactly once."),
				Parameters: shared.FunctionParameters{
					"type": "object",
					"properties": map[string]any{
						"answer": map[string]any{
							"type":        "string",
							"description": "The prose answer, grounded in the provided context. Use Telegram-safe Markdown only: paragraphs, short lists, bold, italic, code, and inline links. Do not use Markdown tables; rewrite tables as concise bullets or labeled lines.",
						},
						"buttons": map[string]any{
							"type": "array",
							"description": "The most relevant sources to surface as tappable buttons. Use ONLY the exact " +
								"source URLs shown in the context; never invent one. Omit when no source has a useful URL.",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"text": map[string]any{
										"type":        "string",
										"description": "Short button label, e.g. the source title.",
									},
									"url": map[string]any{
										"type":        "string",
										"description": "The source URL, copied exactly from the context.",
									},
								},
								"required": []string{"text", "url"},
							},
						},
					},
					"required": []string{"answer"},
				},
			},
		},
	}
}

// submitAnswerArgs mirrors the submit_answer tool parameters.
type submitAnswerArgs struct {
	Answer  string       `json:"answer"`
	Buttons []index.Link `json:"buttons"`
}

// parseSubmitAnswer extracts the answer from a submit_answer tool call, falling
// back to the message content when the model answered in prose. Button URLs are
// filtered to valid http(s) links that appear in allowedURLs (the retrieved
// sources), guarding against hallucinated or unrelated URLs.
func parseSubmitAnswer(msg openai.ChatCompletionMessage, allowedURLs map[string]struct{}) index.Answer {
	for _, tc := range msg.ToolCalls {
		if tc.Type != "function" || tc.Function.Name != submitAnswerToolName {
			continue
		}
		var args submitAnswerArgs
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			continue
		}
		return index.Answer{
			Text:  strings.TrimSpace(args.Answer),
			Links: filterButtons(args.Buttons, allowedURLs),
		}
	}
	return index.Answer{Text: strings.TrimSpace(msg.Content)}
}

// filterButtons keeps only valid http(s) links whose URL is one of the
// retrieved sources, deduplicated by URL and capped at maxAnswerButtons.
func filterButtons(buttons []index.Link, allowedURLs map[string]struct{}) []index.Link {
	if len(buttons) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(buttons))
	var out []index.Link
	for _, b := range buttons {
		b.Text = strings.TrimSpace(b.Text)
		b.URL = strings.TrimSpace(b.URL)
		if !b.Valid() {
			continue
		}
		if _, ok := allowedURLs[b.URL]; !ok {
			continue
		}
		if _, ok := seen[b.URL]; ok {
			continue
		}
		seen[b.URL] = struct{}{}
		out = append(out, b)
		if len(out) >= maxAnswerButtons {
			break
		}
	}
	return out
}

func metaString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

var _ index.Answerer = (*Answerer)(nil)
