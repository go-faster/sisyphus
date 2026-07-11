package answer

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/go-faster/errors"
	"github.com/openai/openai-go/v3"

	"github.com/go-faster/sisyphus/internal/index"
)

func buildSeedMessages(systemPrompt, question string, results []index.Result) ([]openai.ChatCompletionMessageParamUnion, map[string]struct{}, error) {
	tag, err := randomTag()
	if err != nil {
		return nil, nil, errors.Wrap(err, "generate delimiter tag")
	}

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
		if source := metaString(r.Chunk.Metadata, "source"); source != "" {
			fmt.Fprintf(&sb, " [source: %s]", source)
		}
		fmt.Fprintf(&sb, " ---\n%s\n\n", r.Chunk.Text)
	}

	contextBlock := fmt.Sprintf("<<<CONTEXT_%s>>>\n%s<<<END_CONTEXT_%s>>>", tag, sb.String(), tag)
	msgs := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(systemPrompt),
		openai.UserMessage(fmt.Sprintf("Here are initial search results. Use tools to find more if needed.\n\nUntrusted context (between <<<CONTEXT_%s>>> markers):\n%s\n\nQuestion: %s", tag, contextBlock, question)),
	}
	return msgs, allowedURLs, nil
}

func randomTag() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
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
