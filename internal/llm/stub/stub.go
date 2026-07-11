// Package stub provides deterministic, dependency-free stub implementations of
// Summarizer and Answerer interfaces. This allows the system to work end-to-end
// without a real LLM provider (which is deferred). The provider can be swapped
// later without changing the interfaces.
package stub

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/go-faster/sisyphus/internal/index"
)

// Compile-time assertions to ensure implementations satisfy interfaces.
var (
	_ index.Summarizer = Summarizer{}
	_ index.Answerer   = Answerer{}
)

// Summarizer produces deterministic extractive summaries without calling any LLM.
type Summarizer struct{}

// NewSummarizer returns a new Summarizer.
func NewSummarizer() Summarizer {
	return Summarizer{}
}

// Summarize returns a deterministic extractive summary of the prompt.
// It trims, collapses whitespace, and returns the first ~3 sentences or
// first ~500 runes (whichever is shorter), suffixed with " …" if truncated.
func (s Summarizer) Summarize(_ context.Context, prompt string) (string, error) {
	// Normalize: trim and collapse whitespace.
	normalized := strings.TrimSpace(prompt)
	normalized = regexp.MustCompile(`\s+`).ReplaceAllString(normalized, " ")

	if normalized == "" {
		return "", nil
	}

	const (
		maxRunes     = 500
		maxSentences = 3
		truncSuffix  = " …"
	)

	// Split into sentences on periods, question marks, and exclamation marks.
	// Use a simple regex that captures sentence boundaries.
	sentenceRegex := regexp.MustCompile(`[^.!?]*[.!?]+`)
	sentences := sentenceRegex.FindAllString(normalized, -1)

	// Build result by taking up to maxSentences.
	var result strings.Builder
	var runeCount int
	var sentenceCount int

	for _, sent := range sentences {
		if sentenceCount >= maxSentences {
			break
		}

		sentLen := utf8.RuneCountInString(sent)
		if runeCount+sentLen > maxRunes {
			// Adding this sentence would exceed maxRunes; truncate and break.
			// Add as many runes as we can from this sentence up to the limit.
			remaining := maxRunes - runeCount
			if remaining > 0 {
				runeSlice := []rune(sent)
				if remaining < len(runeSlice) {
					result.WriteRune('…')
				}
			}
			return result.String() + truncSuffix, nil
		}

		result.WriteString(sent)
		runeCount += sentLen
		sentenceCount++
	}

	// Check if we've consumed the entire prompt or if there's more text.
	resultStr := result.String()
	if sentenceCount < len(sentences) || runeCount < utf8.RuneCountInString(normalized) {
		// We hit the sentence or rune limit before the end of the prompt.
		return resultStr + truncSuffix, nil
	}

	// We consumed the whole prompt within limits; no truncation needed.
	return resultStr, nil
}

// Answerer constructs a final answer from retrieved context without calling an LLM.
type Answerer struct{}

// NewAnswerer returns a new Answerer.
func NewAnswerer() Answerer {
	return Answerer{}
}

// Answer composes a deterministic, structured response from retrieved results.
// It returns a short header echoing the question, a numbered "Relevant sources"
// list built from each result's Chunk fields, and a "Confidence: low (LLM disabled)"
// footer. If results is empty, it indicates no relevant context was found.
func (a Answerer) Answer(_ context.Context, q index.Query, results []index.Result) (index.Answer, error) {
	var response strings.Builder

	// Header echoing the question.
	response.WriteString("Question: " + q.Text + "\n\n")

	if len(results) == 0 {
		response.WriteString("No relevant context was found to answer this question.\n\n")
		response.WriteString("Confidence: low (LLM disabled)")
		return index.Answer{Text: response.String()}, nil
	}

	// Relevant sources section.
	response.WriteString("Relevant sources:\n")

	for i, result := range results {
		chunk := result.Chunk

		// Numbered entry.
		fmt.Fprintf(&response, "%d. ", i+1)

		// Title and metadata.
		if chunk.Title != "" {
			response.WriteString(chunk.Title)
		} else {
			response.WriteString("(Untitled)")
		}

		// Metadata: source and source_url.
		if source, ok := chunk.Metadata["source"]; ok {
			fmt.Fprintf(&response, " [source: %v]", source)
		}
		if sourceURL, ok := chunk.Metadata["source_url"]; ok {
			fmt.Fprintf(&response, " [%v]", sourceURL)
		}

		response.WriteString("\n")

		// Snippet of text (first ~200 runes).
		const maxSnippetRunes = 200
		snippet := chunk.Text
		if utf8.RuneCountInString(snippet) > maxSnippetRunes {
			runeSlice := []rune(snippet)
			snippet = string(runeSlice[:maxSnippetRunes]) + "…"
		}
		response.WriteString("   " + strings.TrimSpace(snippet) + "\n")
		response.WriteString("\n")
	}

	// Confidence footer.
	response.WriteString("Confidence: low (LLM disabled)")

	return index.Answer{Text: response.String()}, nil
}
