package bot

import (
	"strconv"
	"strings"

	"github.com/go-faster/sisyphus/internal/index"
)

// maxSearchLinks caps how many source-link buttons a /search reply attaches,
// mirroring the report/answer link caps elsewhere in the bot.
const maxSearchLinks = 6

// searchSnippetChars caps each result's text preview so a reply with many
// results still fits comfortably under telegramMessageLimit.
const searchSnippetChars = 240

// searchResultsText formats raw retrieval results as a numbered list for the
// /search command: title + snippet, one result per entry. Unlike /context,
// this is unsummarized retrieval output — no LLM in the loop.
func searchResultsText(results []index.Result) string {
	if len(results) == 0 {
		return "No results found."
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
		sb.WriteString(title)
		sb.WriteString("**\n")
		sb.WriteString(snippet(r.Chunk.Text, searchSnippetChars))
		sb.WriteString("\n\n")
	}
	return strings.TrimSpace(sb.String())
}

// searchLinks builds source-link buttons from results' source_url metadata,
// deduplicated by URL and capped at maxSearchLinks.
func searchLinks(results []index.Result) []index.Link {
	seen := make(map[string]struct{}, len(results))
	var out []index.Link
	for _, r := range results {
		url := metaString(r.Chunk.Metadata, "source_url")
		if url == "" {
			continue
		}
		if _, ok := seen[url]; ok {
			continue
		}
		text := r.Chunk.Title
		if text == "" {
			text = url
		}
		link := index.Link{Text: text, URL: url}
		if !link.Valid() {
			continue
		}
		seen[url] = struct{}{}
		out = append(out, link)
		if len(out) >= maxSearchLinks {
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
