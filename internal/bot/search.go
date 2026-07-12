package bot

import (
	"strconv"
	"strings"

	"github.com/go-faster/sisyphus/internal/index"
)

// searchResultLimit caps how many results a /search reply shows. Kept small
// so the reply reads as a scannable list rather than a wall of text.
const searchResultLimit = 5

// searchSnippetChars caps each result's text preview to roughly two-three
// lines so a reply with several results still fits comfortably under
// telegramMessageLimit.
const searchSnippetChars = 160

// searchResultsText formats raw retrieval results as a numbered list for the
// /search command: title + short snippet + source link, one result per
// entry. Unlike /context, this is unsummarized retrieval output — no LLM in
// the loop. The source link is inlined as Markdown (not a button) so it
// travels with its result when a reply is split across multiple messages.
func searchResultsText(results []index.Result) string {
	if len(results) == 0 {
		return "No results found."
	}
	if len(results) > searchResultLimit {
		results = results[:searchResultLimit]
	}
	var sb strings.Builder
	for i, r := range results {
		title := r.Chunk.Title
		if title == "" {
			title = metaString(r.Chunk.Metadata, "source")
		}
		if title == "" {
			title = "(untitled)"
		}
		sb.WriteString(strconv.Itoa(i + 1))
		sb.WriteString(". **")
		sb.WriteString(escapeMarkdown(title))
		sb.WriteString("**\n")
		sb.WriteString(escapeMarkdown(snippet(r.Chunk.Text, searchSnippetChars)))
		if url := metaString(r.Chunk.Metadata, "source_url"); url != "" {
			link := index.Link{Text: "Source", URL: url}
			if link.Valid() {
				sb.WriteString("\n[Source](")
				sb.WriteString(url)
				sb.WriteString(")")
			}
		}
		sb.WriteString("\n\n")
	}
	return strings.TrimSpace(sb.String())
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

// snippet truncates text to at most max runes, appending an ellipsis when
// truncated. Operating on runes (not bytes) keeps non-ASCII text (e.g.
// Cyrillic) from being cut mid-character.
func snippet(text string, maxRunes int) string {
	text = strings.TrimSpace(text)
	r := []rune(text)
	if len(r) <= maxRunes {
		return text
	}
	return string(r[:maxRunes]) + "…"
}
