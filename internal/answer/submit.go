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
		Text:  strings.TrimSpace(args.Answer),
		Links: filterButtons(args.Buttons, nil),
	}, nil
}

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
		if len(allowedURLs) > 0 {
			if _, ok := allowedURLs[b.URL]; !ok {
				continue
			}
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
