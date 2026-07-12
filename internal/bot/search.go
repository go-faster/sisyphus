package bot

import (
	"context"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/go-faster/errors"
	"github.com/gotd/td/telegram/message/entity"
	"github.com/gotd/td/telegram/message/inline"
	"github.com/gotd/td/telegram/message/markup"
	"github.com/gotd/td/telegram/message/styling"

	"github.com/go-faster/sisyphus/internal/index"
)

const searchResultLimit = 5

const searchPool = 20

const inlineResultLimit = 8

const searchSnippetChars = 160

func dedupResults(results []index.Result, limit int) []index.Result {
	if len(results) == 0 {
		return nil
	}
	seen := make(map[uuid.UUID]struct{}, len(results))
	deduped := make([]index.Result, 0, len(results))
	for _, r := range results {
		if _, ok := seen[r.Chunk.DocumentID]; ok {
			continue
		}
		seen[r.Chunk.DocumentID] = struct{}{}
		deduped = append(deduped, r)
		if len(deduped) >= limit {
			break
		}
	}
	return deduped
}

func (b *Bot) retrieveSearch(ctx context.Context, query string, limit int) ([]index.Result, error) {
	if b.retriever == nil {
		return nil, errors.New("bot retriever is not configured")
	}
	results, err := b.retriever.Retrieve(ctx, index.Query{Text: query, Limit: searchPool})
	if err != nil {
		return nil, errors.Wrap(err, "retrieve")
	}
	return dedupResults(results, limit), nil
}

func formatSearchResult(r index.Result, i int) string {
	title := r.Chunk.Title
	if title == "" {
		title = metaString(r.Chunk.Metadata, "source")
	}
	if title == "" {
		title = "(untitled)"
	}

	var sb strings.Builder
	sb.WriteString(strconv.Itoa(i + 1))
	sb.WriteString(". **")
	sb.WriteString(escapeMarkdown(title))
	sb.WriteString("**\n```\n")
	sb.WriteString(snippet(r.Chunk.Text, searchSnippetChars))
	sb.WriteString("\n```")
	if url := metaString(r.Chunk.Metadata, "source_url"); url != "" {
		link := index.Link{Text: "Source", URL: url}
		if link.Valid() {
			sb.WriteString("\n[Source](")
			sb.WriteString(url)
			sb.WriteString(")")
		}
	}
	return sb.String()
}

func searchResultsText(results []index.Result) string {
	if len(results) == 0 {
		return "No results found."
	}
	if len(results) > searchResultLimit {
		results = results[:searchResultLimit]
	}
	var parts []string
	for i, r := range results {
		parts = append(parts, formatSearchResult(r, i))
	}
	return strings.Join(parts, "\n\n")
}

func searchInlineResults(results []index.Result) []inline.ResultOption {
	opts := make([]inline.ResultOption, 0, len(results))
	for i, r := range results {
		title := r.Chunk.Title
		if title == "" {
			title = metaString(r.Chunk.Metadata, "source")
		}
		if title == "" {
			title = "(untitled)"
		}
		titleRunes := []rune(title)
		if len(titleRunes) > 80 {
			title = string(titleRunes[:80]) + "\u2026"
		}

		desc := snippet(r.Chunk.Text, searchSnippetChars)
		formatted := formatSearchResult(r, i)

		msgOpt := inline.MessageStyledText(styling.Custom(func(eb *entity.Builder) error {
			return renderMarkdown(eb, formatted)
		}))

		var sourceURL string
		if url := metaString(r.Chunk.Metadata, "source_url"); url != "" {
			if l := (index.Link{Text: "Source", URL: url}); l.Valid() {
				msgOpt = msgOpt.Row(markup.URL("Source", url))
				sourceURL = url
			}
		}

		article := inline.Article(title, msgOpt)
		article = article.ID(r.Chunk.ID.String()).Description(desc)
		if sourceURL != "" {
			article = article.URL(sourceURL)
		}

		opts = append(opts, article)
	}
	return opts
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

func snippet(text string, maxRunes int) string {
	text = strings.TrimSpace(text)
	r := []rune(text)
	if len(r) <= maxRunes {
		return text
	}
	return string(r[:maxRunes]) + "\u2026"
}
