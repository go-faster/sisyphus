package answer

import (
	"encoding/json"
	"strings"

	"github.com/go-faster/errors"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"

	"github.com/go-faster/sisyphus/internal/index"
)

const maxAnswerButtons = 6

const submitAnswerToolName = "submit_answer"

type submitAnswerArgs struct {
	Answer  string       `json:"answer"`
	Buttons []index.Link `json:"buttons"`
}

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
							"type":        "array",
							"description": "The most relevant sources to surface as tappable buttons. Use ONLY the exact source URLs shown in the context; never invent one. Omit when no source has a useful URL.",
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

func parseSubmitAnswer(argsJSON string) (index.Answer, error) {
	var args submitAnswerArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return index.Answer{}, errors.Wrap(err, "unmarshal submit answer")
	}
	return index.Answer{
		Text: strings.TrimSpace(args.Answer),
		// Not the final, allowlist-constrained set: the loop.go call site
		// re-filters through filterButtons once it knows the full allowed-URL
		// set (seed sources + URLs discovered mid-loop). This intermediate
		// value only trims/validates/dedups/caps.
		Links: sanitizeButtons(args.Buttons),
	}, nil
}

// sanitizeButtons trims, validates, deduplicates by URL, and caps at
// maxAnswerButtons. It applies no allowlist — use filterButtons instead
// wherever a button's URL must be constrained to vetted sources.
func sanitizeButtons(buttons []index.Link) []index.Link {
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

// filterButtons sanitizes buttons (see sanitizeButtons) and constrains them
// to allowedURLs. This enforces the "buttons only link to vetted sources"
// guarantee: unlike a naive implementation, a nil or empty allowedURLs
// rejects every button rather than silently skipping the check — there is
// no way to call this and disable the allowlist.
func filterButtons(buttons []index.Link, allowedURLs map[string]struct{}) []index.Link {
	sanitized := sanitizeButtons(buttons)
	if len(sanitized) == 0 {
		return nil
	}
	var out []index.Link
	for _, b := range sanitized {
		if _, ok := allowedURLs[b.URL]; !ok {
			continue
		}
		out = append(out, b)
	}
	return out
}
